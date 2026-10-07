package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/nebula/monitor/internal/server/audit"
)

// auditDedupKey 用于标记某次请求已由 handler 显式记录审计，避免中间件重复记录。
type auditDedupKey struct{}

// requestIDKey 是本次操作的关联 id（见 RequestID）。
type requestIDKey struct{}

// RequestID 返回本次请求的关联 id；空串表示"这次请求不会有审计行"。
//
// 它由审计中间件在**执行 handler 之前**生成，这一点是刻意的：审计行要等 handler 返回、
// 拿到状态码之后才写得出来，所以"先取审计 id、再写进变更记录"这条路走不通（写变更时
// 审计行还不存在）；反过来先给请求发一个 id，两侧各写一份，关联就成立了——不依赖写入
// 顺序，也就不需要跨包事务。
//
// 只读请求、登录与 Agent 上报没有值：它们不会有审计行，发了 id 只会造出指向空的关联。
func RequestID(r *http.Request) string {
	if v, ok := r.Context().Value(requestIDKey{}).(string); ok {
		return v
	}
	return ""
}

// newRequestID 生成关联 id：8 字节随机数的十六进制。
//
// 刻意不用"时间戳+序号"：那种 id 把"什么时候发生的"编码进去，看着既能排序又能当索引，
// 实际上会让人以为它可靠（多实例、时钟回拨都对不上）。它只需要够随机、不撞。
func newRequestID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// 随机源不可用时不退化成可预测或可重复的值：宁可没有关联，也不给出一个可能重复的
		// id——重复会把两次不同的操作混成一次，那是"看起来像证据"的错。
		return ""
	}
	return hex.EncodeToString(buf[:])
}

// maxAuditBodyBytes 审计中间件记录请求体时的最大字节数（约 1MB），超出部分截断。
const maxAuditBodyBytes = 1 << 20

// AuditMiddleware 记录认证后的管理写请求，避免把查询参数和请求体敏感值写入审计日志。
func AuditMiddleware(next http.Handler, store *audit.Store) http.Handler {
	if store == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 只记录管理操作：跳过只读、登录以及 Agent 数据上报接口。
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.URL.Path == "/api/v1/login" || r.URL.Path == "/api/v1/report" {
			next.ServeHTTP(w, r)
			return
		}
		// 跳过文件上传类接口（升级包 / GeoIP 库等）：这类请求体大、耗时长（解压），
		// 若纳入审计会拖慢响应返回，导致反代 / 网闸隧道（ZMQ）超时 RST（ERR_CONNECTION_RESET）；
		// 且上传本身不产生有意义的审计摘要，故完全排除，不写审计。
		if strings.HasSuffix(r.URL.Path, "/upload") {
			next.ServeHTTP(w, r)
			return
		}
		// 关联 id：能走到这里说明本次请求会被审计（下面必然写一条），因此发一个 id，
		// 同时给审计行与本次操作产生的资产变更记录用。放在跳过判断**之后**，
		// 为的是让"有 id 的变更 ⇔ 有一次可查的审计"这个不变式成立。
		requestID := newRequestID()
		// 标记本请求：若后续 handler 已显式调用 RecordChangeAudit 记录（含变更摘要），
		// 则中间件不再重复记录，避免同一次操作产生两条审计（如规则增删改）。
		recorded := new(bool)
		ctx := context.WithValue(r.Context(), requestIDKey{}, requestID)
		r = r.WithContext(context.WithValue(ctx, auditDedupKey{}, recorded))
		// multipart/form-data（升级包 / GeoIP 库上传等）不能提前读取并还原请求体：
		// io.MultiReader 还原会让下游 ParseMultipartForm 报 "bufio: buffer full"，
		// 导致上传解析失败。上传类请求本就不会被审计记录（classify 返回空 action），故跳过读取。
		var detail string
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			detail = auditRequestDetail(r)
		}
		recorder := &auditResponseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		if *recorded {
			return
		}
		if recorder.status >= 200 && recorder.status < 400 {
			detail = enrichChangeDetail(r, detail)
		}
		_ = store.Record(audit.Event{
			User:      AuthenticatedUser(r),
			Method:    r.Method,
			Path:      audit.RedactPath(r),
			Status:    recorder.status,
			RemoteIP:  audit.ClientIP(r),
			Succeeded: recorder.status >= 200 && recorder.status < 400,
			RequestID: requestID,
			Category:  "management",
			Action:    managementAction(r.Method, r.URL.Path),
			Detail:    detail,
		})
	})
}

func auditRequestDetail(r *http.Request) string {
	if r.Body == nil || r.ContentLength == 0 {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxAuditBodyBytes+1))
	if err != nil {
		return "request_read_error"
	}
	if len(body) > maxAuditBodyBytes {
		return "request_body=oversize"
	}
	r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))
	var payload interface{}
	if json.Unmarshal(body, &payload) != nil {
		return fmt.Sprintf("request_sha256=%s", shortHash(body))
	}
	fields := make([]string, 0)
	if object, ok := payload.(map[string]interface{}); ok {
		for key := range object {
			fields = append(fields, key)
		}
		sort.Strings(fields)
	}
	return fmt.Sprintf("request_fields=%s request_sha256=%s", strings.Join(fields, ","), shortHash(body))
}

// RecordChangeAudit 记录持久化对象的语义化变更摘要，不保存对象原文。
// 同时标记本次请求已由 handler 记录，避免 AuditMiddleware 重复记录同一次操作。
func RecordChangeAudit(store *audit.Store, r *http.Request, action string, before, after interface{}) {
	if store == nil {
		return
	}
	if v, ok := r.Context().Value(auditDedupKey{}).(*bool); ok {
		*v = true
	}
	_ = store.Record(audit.Event{
		User:      AuthenticatedUser(r),
		Method:    r.Method,
		Path:      audit.RedactPath(r),
		Status:    http.StatusOK,
		RemoteIP:  audit.ClientIP(r),
		Succeeded: true,
		RequestID: RequestID(r),
		Category:  "management",
		Action:    action,
		Detail:    audit.SummarizeChange(before, after),
	})
}

func enrichChangeDetail(r *http.Request, detail string) string {
	if !strings.Contains(r.URL.Path, "/rules") && !strings.Contains(r.URL.Path, "/nodes") &&
		!strings.Contains(r.URL.Path, "/config") && !strings.Contains(r.URL.Path, "/assets") {
		return detail
	}
	if detail == "" {
		return "change=" + managementAction(r.Method, r.URL.Path)
	}
	return detail + " change=" + managementAction(r.Method, r.URL.Path)
}

func shortHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:16]
}

func managementAction(method, path string) string {
	verb := map[string]string{
		http.MethodPost:   "create_or_apply",
		http.MethodPut:    "update",
		http.MethodPatch:  "update",
		http.MethodDelete: "delete",
	}[method]
	if verb == "" {
		verb = strings.ToLower(method)
	}
	return verb + " " + path
}

// RecordLoginAudit 写入登录结果，不记录密码、Token 或请求体。
func RecordLoginAudit(store *audit.Store, r *http.Request, user string, status int, succeeded bool) {
	if store == nil {
		return
	}
	_ = store.Record(audit.Event{
		User:      user,
		Method:    http.MethodPost,
		Path:      "/api/v1/login",
		Status:    status,
		RemoteIP:  audit.ClientIP(r),
		Succeeded: succeeded,
		Category:  "authentication",
		Action:    "login",
	})
}

// RecordPermissionDenied 记录权限拒绝（不记录敏感内容）。
func RecordPermissionDenied(store *audit.Store, r *http.Request, user, perm string) {
	if store == nil {
		return
	}
	_ = store.Record(audit.Event{
		User:      user,
		Method:    r.Method,
		Path:      r.URL.Path,
		Status:    http.StatusForbidden,
		RemoteIP:  audit.ClientIP(r),
		Succeeded: false,
		RequestID: RequestID(r),
		Category:  "authorization",
		Action:    "deny",
		Detail:    "permission=" + perm,
	})
}

type auditResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *auditResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *auditResponseWriter) Write(data []byte) (int, error) {
	return w.ResponseWriter.Write(data)
}
