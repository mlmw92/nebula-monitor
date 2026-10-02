package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/nebula/monitor/internal/server/asset"
)

// 人工维护资产关联（关系的写接口）。
//
// 三种动作（建 / 删 / 取消抑制）共用**一套寻址**：URL 里的资产是基准，给
// {toType,toKey,kind,direction}。direction=out（默认）表示「基准 → 对端」，
// in 表示「对端 → 基准」。读出接口本来就返回 direction，前端把它原样回传即可 ——
// 这样既不用前端自己算方向，也不会出现「我以为是出边、其实删的是入边」。
//
// 寻址的**来源**按方法分开，这是刻意的：
//   - POST 从 JSON 体读（常规写法）；
//   - DELETE 只从**查询串**读。DELETE 的请求体在 HTTP 语义里没有定义，中间设备丢弃它是
//     合法行为，把寻址放在体里等于埋一个"某些环境下删不掉"的坑，而且极难现场排查。
// 不做"哪个有值用哪个"的兜底猜测：来源不明确会让调用方无从判断自己写错了没有。
//
// 为什么删除是**逻辑删除**（实现见 asset.Service.UnlinkManual）：关系多半是采集发现的，
// 物理删掉下一轮采集立刻把它建回来，用户的操作等于没做。删除会落一条抑制记录，
// 采集侧据此不再重建；想撤回这个判断用 /links/restore。
//
// 三个接口共用 assets:write —— 它就是「台账维护」这个动作的权限点，与忽略/标签/标杆同级，
// 关系并不比它们更敏感或更不敏感。

type assetLinkWriteBody struct {
	// ToType/ToKey 定位对端资产（与其它资产接口一致：类型 + 自然键）。
	ToType string `json:"toType"`
	ToKey  string `json:"toKey"`
	// Kind 是关系类型，取 asset.LinkKind 的白名单。
	Kind string `json:"kind"`
	// Direction 见文件头说明；留空按 out 处理。
	Direction string `json:"direction"`
}

// decodeAssetLinkTarget 解析并校验寻址参数，返回（from, to, kind）。
//
// 出错时自己写好响应并返回 ok=false，与 assetInScope 的约定一致 ——
// 调用方只需要 `if !ok { return }`。
func (a *API) decodeAssetLinkTarget(w http.ResponseWriter, r *http.Request, item asset.Asset) (asset.Ref, asset.Ref, asset.LinkKind, bool) {
	q := r.URL.Query()
	body := assetLinkWriteBody{
		ToType: q.Get("toType"), ToKey: q.Get("toKey"),
		Kind: q.Get("kind"), Direction: q.Get("direction"),
	}
	if r.Method != http.MethodDelete && r.Body != nil {
		var fromBody assetLinkWriteBody
		if err := json.NewDecoder(r.Body).Decode(&fromBody); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
			return asset.Ref{}, asset.Ref{}, "", false
		}
		body = fromBody
	}
	kind := asset.LinkKind(strings.TrimSpace(body.Kind))
	if !kind.Valid() {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("未知的资产关联类型: %q", body.Kind),
		})
		return asset.Ref{}, asset.Ref{}, "", false
	}
	toType, toKey := strings.TrimSpace(body.ToType), strings.TrimSpace(body.ToKey)
	if toType == "" || toKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "必须给出对端资产的 toType 与 toKey"})
		return asset.Ref{}, asset.Ref{}, "", false
	}
	self := assetRefOf(item)
	peer := asset.Ref{TypeKey: toType, NaturalKey: toKey}
	switch strings.TrimSpace(body.Direction) {
	case "", "out":
		return self, peer, kind, true
	case "in":
		return peer, self, kind, true
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("direction 只支持 out / in，实际 %q", body.Direction),
		})
		return asset.Ref{}, asset.Ref{}, "", false
	}
}

// ensureLinkPeerVisible 校验对端资产存在且在资源范围内。
//
// 写接口不能成为绕过范围约束的口子：否则受限用户可以用「建一条指向范围外资产的边」
// 是否成功来确认那个资产是否存在 —— 读接口把范围外对端整条剔除，正是为了不给这个信息。
func (a *API) ensureLinkPeerVisible(w http.ResponseWriter, r *http.Request, peer asset.Ref) bool {
	item, found, err := a.assets.Get(peer)
	if err != nil {
		slog.Error("查询关联对端资产失败", "asset", peer.NaturalKey, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询关联对端资产失败"})
		return false
	}
	// 统一用 404 而不是区分「不存在」与「不在范围内」：两者对调用方是同一件事，
	// 区分开就等于把范围外资产的存在性告诉了他。
	if !found || !a.nodeInScope(Principal(r), item.Node) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "对端资产不存在或不在可见范围内"})
		return false
	}
	return true
}

// handleAssetLinkCreate 人工建立一条关联。
//
// 幂等；若这条边此前由采集建立，本次会把它**升级为人工认领**（source 变 manual），
// 之后采集继续上报同一条边也不会把它降级回来。
func (a *API) handleAssetLinkCreate(w http.ResponseWriter, r *http.Request) {
	item, ok := a.assetInScope(w, r)
	if !ok {
		return
	}
	from, to, kind, ok := a.decodeAssetLinkTarget(w, r, item)
	if !ok {
		return
	}
	if !a.ensureLinkPeerVisible(w, r, to) {
		return
	}
	if err := a.assets.LinkManual(from, to, kind); err != nil {
		slog.Error("人工建立资产关联失败", "asset", item.NaturalKey, "err", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	RecordChangeAudit(a.audit, r, "asset.link.create", nil, map[string]interface{}{
		"fromType": from.TypeKey, "fromKey": from.NaturalKey,
		"toType": to.TypeKey, "toKey": to.NaturalKey,
		"kind": string(kind), "source": string(asset.SourceManual),
	})
	a.writeAssetLinksPayload(w, r, item)
}

// handleAssetLinkDelete 人工解除一条关联 —— 逻辑删除（删边 + 落抑制）。
//
// 对采集发现的边：删掉之后采集不会再重建，因为人已经表态「这条关系不存在」。
// 对人工自己建的边同理 —— 保留抑制比区分来源更稳：无论这条边当初是谁建的，
// 「人工说它不存在」这个判断都应当生效，否则它会被采集悄悄复活。
func (a *API) handleAssetLinkDelete(w http.ResponseWriter, r *http.Request) {
	item, ok := a.assetInScope(w, r)
	if !ok {
		return
	}
	from, to, kind, ok := a.decodeAssetLinkTarget(w, r, item)
	if !ok {
		return
	}
	if !a.ensureLinkPeerVisible(w, r, to) {
		return
	}
	if err := a.assets.UnlinkManual(from, to, kind, assetActor(r)); err != nil {
		slog.Error("人工解除资产关联失败", "asset", item.NaturalKey, "err", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	RecordChangeAudit(a.audit, r, "asset.link.delete", map[string]interface{}{
		"fromType": from.TypeKey, "fromKey": from.NaturalKey,
		"toType": to.TypeKey, "toKey": to.NaturalKey,
		"kind": string(kind),
	}, map[string]interface{}{"suppressed": true, "by": assetActor(r)})
	a.writeAssetLinksPayload(w, r, item)
}

// handleAssetLinkRestore 取消人工抑制，允许采集重新建立这条边（幂等）。
//
// 这是逻辑删除的出口：没有它，用户删错一次就再也回不到「让采集自己管这条关系」的状态。
func (a *API) handleAssetLinkRestore(w http.ResponseWriter, r *http.Request) {
	item, ok := a.assetInScope(w, r)
	if !ok {
		return
	}
	from, to, kind, ok := a.decodeAssetLinkTarget(w, r, item)
	if !ok {
		return
	}
	if !a.ensureLinkPeerVisible(w, r, to) {
		return
	}
	if err := a.assets.RestoreDiscovered(from, to, kind); err != nil {
		slog.Error("取消资产关联抑制失败", "asset", item.NaturalKey, "err", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	RecordChangeAudit(a.audit, r, "asset.link.restore", map[string]interface{}{
		"suppressed": true,
	}, map[string]interface{}{
		"fromType": from.TypeKey, "fromKey": from.NaturalKey,
		"toType": to.TypeKey, "toKey": to.NaturalKey,
		"kind": string(kind), "suppressed": false,
	})
	a.writeAssetLinksPayload(w, r, item)
}

// writeAssetLinksPayload 写操作后统一回填最新关联载荷。
func (a *API) writeAssetLinksPayload(w http.ResponseWriter, r *http.Request, item asset.Asset) {
	payload, err := a.assetLinksPayload(item, Principal(r))
	if err != nil {
		slog.Error("回读资产关联失败", "asset", item.NaturalKey, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "回读资产关联失败"})
		return
	}
	writeJSON(w, http.StatusOK, payload)
}
