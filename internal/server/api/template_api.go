package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	dsl "github.com/nebula/monitor/internal/template"
)

// TemplatesProvider 提供采集项模板 CRUD（由 templates 包实现）。
//
// 声明为接口而非具体类型：API 层不依赖存储实现，测试可注入替身；
// 与 dialtest 等既有模块保持同一种接线方式。
type TemplatesProvider interface {
	List() []dsl.Config
	Get(id string) (dsl.Config, bool)
	Upsert(cfg dsl.Config) error
	Validate(cfg dsl.Config) error
	Delete(id string) error
	Revision() uint64
}

// handleTemplatesList 返回全部模板与当前版本号。
// 版本号一并返回：前端可据此判断「是否有变更待下发」，也便于排查「改了没生效」。
func (a *API) handleTemplatesList(w http.ResponseWriter, r *http.Request) {
	if a.templates == nil {
		writeJSON(w, 200, map[string]interface{}{"templates": []templateView{}, "revision": 0})
		return
	}
	list := a.templates.List()
	out := make([]templateView, 0, len(list))
	for _, cfg := range list {
		out = append(out, a.toView(cfg))
	}
	writeJSON(w, 200, map[string]interface{}{
		"templates": out,
		"revision":  a.templates.Revision(),
	})
}

// templateView 是模板的读视图：在配置之上附带「对多少个节点无效」的统计。
type templateView struct {
	dsl.Config
	// Targets 覆盖嵌入的同名字段，附上「该目标是否已配置凭据」的提示。
	Targets []targetView `json:"targets"`
	// IneffectiveNodes 是「配置了这个模板、但本机未放行该取数方式」的采集节点数。
	//
	// 为什么必须给出这个数字：护栏类取数方式（jdbc/exec/file）要在各机器自己的 agent.yaml 里放行，
	// 未放行的节点**根本收不到**该模板。不提示的话，用户看到的就是「模板建好了却没有数据」，
	// 而原因（要逐台机器放行）只写在 Agent 日志里。非护栏类恒为 0（任何节点都能执行）。
	IneffectiveNodes int `json:"ineffectiveNodes,omitempty"`
}

// targetView 是目标的读视图。
//
// auth 不带 json tag（凭据永不回显），因此这里额外给出 HasAuth：
// 否则用户在编辑界面上看不到任何凭据痕迹，会以为「凭据丢了」甚至重新手填一遍——
// 而重填时只要有一个字段没对上，服务端就不会自动保留原凭据。
type targetView struct {
	dsl.Target
	HasAuth bool `json:"hasAuth,omitempty"`
}

// toView 生成模板读视图：**抹掉凭据**再序列化，并附上「未放行节点数」。
//
// 所有面向界面的响应都必须走这里。凭据是只写不读字段（见 template.Target.Auth 的注释）：
// 下发路径需要它、写入需要它，但界面回显会把密码暴露在浏览器、日志与截图里。
func (a *API) toView(cfg dsl.Config) templateView {
	// 复制一份再抹掉凭据：直接改 cfg.Targets[i] 会污染调用方手上的配置（存储里的那一份）
	safe := cfg
	safe.Targets = make([]dsl.Target, 0, len(cfg.Targets))
	targets := make([]targetView, 0, len(cfg.Targets))
	for _, t := range cfg.Targets {
		hasAuth := t.Auth != nil
		t.Auth = nil // t 是循环副本，改它不影响 cfg
		safe.Targets = append(safe.Targets, t)
		targets = append(targets, targetView{Target: t, HasAuth: hasAuth})
	}
	return templateView{Config: safe, Targets: targets, IneffectiveNodes: a.ineffectiveNodes(cfg)}
}

// ineffectiveNodes 统计该模板覆盖范围内「本机未放行其取数方式」的采集节点数。
//
// 只统计采集节点（ListHostNodes）：edge/hub 是网闸代理，不执行模板；
// 把「本机是否放行」当成配置问题而不是故障——它不会自愈，必须由人来改 agent.yaml。
func (a *API) ineffectiveNodes(cfg dsl.Config) int {
	if a.nodeMgr == nil || !dsl.IsGuardedKind(cfg.Kind) || len(cfg.Groups) == 0 {
		return 0
	}
	count := 0
	for _, n := range a.nodeMgr.ListHostNodes() {
		if !slices.Contains(cfg.Groups, n.Group) {
			continue
		}
		if !dsl.KindEnabledOnNode(cfg.Kind, n.TemplateKinds) {
			count++
		}
	}
	return count
}

// handleTemplateCreate 新建模板。
//
// id 已存在时返回 409 而不是覆盖：模板 id 同时是指标名前缀，覆盖会静默改变已落库指标的归属，
// 因此「新建」与「修改」必须走不同入口，避免误以为在新建。
func (a *API) handleTemplateCreate(w http.ResponseWriter, r *http.Request) {
	cfg, ok := decodeTemplateBody(w, r)
	if !ok {
		return
	}
	if a.templates == nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody("模板存储不可用"))
		return
	}
	if _, exists := a.templates.Get(cfg.ID); exists {
		writeJSON(w, http.StatusConflict, errBody("模板 id "+cfg.ID+" 已存在；如需修改请使用更新"))
		return
	}
	if err := a.templates.Upsert(cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	// 回读视图而不是 cfg：凭据只写不读，响应里不得回显（见 toView）
	writeJSON(w, 200, a.toView(cfg))
}

// handleTemplateUpdate 更新模板。
//
// 不允许改 id：id 决定指标名前缀，改名等价于换一套指标（历史序列会断），
// 这种操作应走「删除 + 新建」让调用方显式确认。
func (a *API) handleTemplateUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cfg, ok := decodeTemplateBody(w, r)
	if !ok {
		return
	}
	if a.templates == nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody("模板存储不可用"))
		return
	}
	if cfg.ID != "" && cfg.ID != id {
		writeJSON(w, http.StatusBadRequest, errBody("不允许修改模板 id（"+id+" → "+cfg.ID+"）：id 决定指标名前缀，请改用「删除 + 新建」"))
		return
	}
	cfg.ID = id
	old, exists := a.templates.Get(id)
	if !exists {
		writeJSON(w, http.StatusNotFound, errBody("模板 "+id+" 不存在"))
		return
	}
	// 凭据永不回显（target.auth 不打 json tag），因此编辑保存时入参里没有 auth。
	// 这里按「目标身份」把原有凭据补回：不做这一步的后果是「编辑一次就把库密码静默清空」——
	// 下轮采集才开始失败，而失败原因看起来像连不上库，比直接报错难查得多。
	cfg.Targets = preserveTargetAuth(cfg.Targets, old.Targets)
	if err := a.templates.Upsert(cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	// 回读视图而不是 cfg：凭据只写不读，响应里不得回显（见 toView）
	writeJSON(w, 200, a.toView(cfg))
}

// preserveTargetAuth 把入参中缺失的凭据按目标身份从旧配置里补回。
//
// 规则刻意保守：只在**目标身份完全一致**（instance/addr/command/path 全同）且旧配置确有凭据时补回；
// 身份变了（例如换了库地址）就不搬凭据——把 A 的密码悄悄用到 B 上比丢掉它更糟。
// 入参显式给出 auth 时以入参为准（支持换密码）——只是当前没有任何接口会把旧凭据回显给用户。
func preserveTargetAuth(in, old []dsl.Target) []dsl.Target {
	for i := range in {
		if in[i].Auth != nil {
			continue
		}
		for j := range old {
			if old[j].Auth != nil && sameTargetIdentity(in[i], old[j]) {
				in[i].Auth = old[j].Auth
				break
			}
		}
	}
	return in
}

// sameTargetIdentity 判断两个目标是否为同一个（凭据之外的定位字段全部相同）。
func sameTargetIdentity(a, b dsl.Target) bool {
	return a.Instance == b.Instance && a.Addr == b.Addr &&
		a.Command == b.Command && a.Path == b.Path
}

// handleTemplateDelete 删除模板。
func (a *API) handleTemplateDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if a.templates == nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody("模板存储不可用"))
		return
	}
	if err := a.templates.Delete(id); err != nil {
		writeJSON(w, http.StatusNotFound, errBody("模板 "+id+" 不存在"))
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

// handleTemplatePresets 返回内置预设（常见中间件的开箱模板）。
//
// 预设只是「待填模板」：groups 为空（由用户按自己环境选择），目标地址是本机默认端口。
// 它解决的是「不知道 exporter 有哪些指标、keep/drop 该怎么写」这个真实门槛——
// 这些规则逐一核对过真实 exporter 的输出形态（见 internal/template/presets.go）。
func (a *API) handleTemplatePresets(w http.ResponseWriter, r *http.Request) {
	presets := dsl.Presets()
	out := make([]map[string]interface{}, 0, len(presets))
	for _, p := range presets {
		out = append(out, map[string]interface{}{
			"id":     p.ID,
			"title":  p.Title,
			"desc":   p.Desc,
			"note":   p.Note,
			"config": p.Config,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"presets": out})
}

// handleTemplateValidate 只校验不保存，返回与保存时一致的校验结果。
//
// 存在的意义：校验规则（含 id 互为前缀、保留指标族、正则、标签越权等）都在 Go 侧，
// 前端无法自行判定；由本接口把**精确原因**交给编辑器，用户不必「保存失败再猜」。
func (a *API) handleTemplateValidate(w http.ResponseWriter, r *http.Request) {
	cfg, ok := decodeTemplateBody(w, r)
	if !ok {
		return
	}
	if a.templates == nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody("模板存储不可用"))
		return
	}
	if err := a.templates.Validate(cfg); err != nil {
		// errors.Join 的多条错误按行分隔，逐条返回便于前端列表展示
		writeJSON(w, 200, map[string]interface{}{
			"ok":     false,
			"errors": strings.Split(err.Error(), "\n"),
		})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true, "errors": []string{}})
}

// decodeTemplateBody 解析请求体中的模板配置。
// 解析失败时返回给前端的文案包含具体原因（前端 http.js 会优先展示 error 字段）。
func decodeTemplateBody(w http.ResponseWriter, r *http.Request) (dsl.Config, bool) {
	var cfg dsl.Config
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("请求体不是合法的模板 JSON："+err.Error()))
		return cfg, false
	}
	return cfg, true
}

// errBody 统一错误响应体（前端 http.js 读取 error 字段展示）。
func errBody(msg string) map[string]string {
	return map[string]string{"error": msg}
}
