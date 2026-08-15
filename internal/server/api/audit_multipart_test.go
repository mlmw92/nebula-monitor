package api

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nebula/monitor/internal/server/audit"
)

// TestAuditMiddlewareDoesNotBreakMultipartUpload 回归测试：
// 审计中间件不得读取并还原 multipart 上传请求体，否则会破坏下游 ParseMultipartForm
// （报 "bufio: buffer full"），导致升级包 / GeoIP 库上传失败。
func TestAuditMiddlewareDoesNotBreakMultipartUpload(t *testing.T) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "nebula-monitor-v1.2.3-upgrade.tar.gz")
	payload := bytes.Repeat([]byte("X"), 8*1024*1024) // 8MB，远超审计读取阈值
	fw.Write(payload)
	mw.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/system/upgrade/upload", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(500 << 20); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("parse err: " + err.Error()))
			return
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("formfile err: " + err.Error()))
			return
		}
		defer f.Close()
		got, _ := io.ReadAll(f)
		if len(got) != len(payload) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("size mismatch"))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	store := audit.New("") // 内存审计，不落盘
	handler := AuditMiddleware(mux, store)

	req := httptest.NewRequest("POST", "/api/v1/system/upgrade/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.ContentLength = int64(buf.Len())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d，body=%s", rec.Code, rec.Body.String())
	}
}
