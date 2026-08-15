package api

import (
	"bytes"
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
		detail := auditRequestDetail(r)
		recorder := &auditResponseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
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
func RecordChangeAudit(store *audit.Store, r *http.Request, action string, before, after interface{}) {
	if store == nil {
		return
	}
	_ = store.Record(audit.Event{
		User:      AuthenticatedUser(r),
		Method:    r.Method,
		Path:      audit.RedactPath(r),
		Status:    http.StatusOK,
		RemoteIP:  audit.ClientIP(r),
		Succeeded: true,
		Category:  "management",
		Action:    action,
		Detail:    audit.SummarizeChange(before, after),
	})
}

func enrichChangeDetail(r *http.Request, detail string) string {
	if !strings.Contains(r.URL.Path, "/rules") && !strings.Contains(r.URL.Path, "/nodes") && !strings.Contains(r.URL.Path, "/config") {
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
