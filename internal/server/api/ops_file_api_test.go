package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/auth"
	"github.com/nebula/monitor/internal/server/ops"
)

// 文件分发的完整入口链路：上传拿到引用 → 用引用建任务 → 内容由服务端保管、任务里只有引用。
func TestOpsFileUploadThenDispatch(t *testing.T) {
	a, svc := opsTestAPI(t)
	global := globalPrincipal("ops:exec")

	upload := func(name string, body []byte) *httptest.ResponseRecorder {
		t.Helper()
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("file", name)
		_, _ = fw.Write(body)
		_ = mw.Close()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/ops/files", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req = req.WithContext(context.WithValue(req.Context(), principalContextKey{}, global))
		w := httptest.NewRecorder()
		a.handleOpsFileUpload(w, req)
		return w
	}

	content := []byte("server {\n  listen 8080;\n}\n")
	w := upload("nginx.conf", content)
	if w.Code != http.StatusCreated {
		t.Fatalf("上传状态码 = %d，响应 %s", w.Code, w.Body.String())
	}
	var rec ops.FileRecord
	if err := json.Unmarshal(w.Body.Bytes(), &rec); err != nil {
		t.Fatalf("解析上传响应失败: %v", err)
	}
	sum := sha256.Sum256(content)
	if rec.Ref == "" || rec.Size != int64(len(content)) || rec.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("上传记录不符：%+v", rec)
	}
	if rec.Name != "nginx.conf" {
		t.Fatalf("应保留文件名，实际 %q", rec.Name)
	}

	// 超限：错误信息必须自己说清上限，而不是让用户去猜
	if got := upload("big.bin", make([]byte, model.OpsFileMaxBytes+1)); got.Code != http.StatusBadRequest {
		t.Fatalf("超限应返回 400，实际 %d", got.Code)
	} else if !strings.Contains(got.Body.String(), "上限") {
		t.Fatalf("超限提示应说明上限，实际 %s", got.Body.String())
	}
	// 空文件同样拒绝
	if got := upload("empty.conf", nil); got.Code != http.StatusBadRequest {
		t.Fatalf("空文件应返回 400，实际 %d", got.Code)
	}

	// 节点声明了该能力，才谈得上下发
	svc.Store().SaveCaps("web-01", []string{ops.KindFilePush})

	create := func(params map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		req := assetWriteReq(global, http.MethodPost, "/api/v1/ops/tasks", opsCreateBody{
			Node: "web-01", Kind: ops.KindFilePush, Params: params, Reason: "改端口",
		})
		w := httptest.NewRecorder()
		a.handleOpsCreate(w, req)
		return w
	}

	ok := create(map[string]string{"path": "/opt/app/conf/app.conf", "fileId": rec.Ref})
	if ok.Code != http.StatusCreated {
		t.Fatalf("下发状态码 = %d，响应 %s", ok.Code, ok.Body.String())
	}
	// 任务记录里带上文件元信息快照：任务列表要能回答"分发的是哪个文件"，
	// 而内容本身会被淘汰——记录不能因此退化成一串看不懂的引用号。
	var view struct {
		Task struct {
			File *ops.FileRecord `json:"file"`
		} `json:"task"`
	}
	if err := json.Unmarshal(ok.Body.Bytes(), &view); err != nil {
		t.Fatalf("解析任务响应失败: %v", err)
	}
	if view.Task.File == nil || view.Task.File.Ref != rec.Ref || view.Task.File.Name != "nginx.conf" {
		t.Fatalf("任务应带文件元信息快照，实际 %+v", view.Task.File)
	}

	// 引用不存在：400（输入错），且提示里要有那个引用号，便于对账
	bad := create(map[string]string{"path": "/opt/app/conf/app.conf", "fileId": "obf-999"})
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("不存在的文件引用应返回 400，实际 %d", bad.Code)
	}
	if !strings.Contains(bad.Body.String(), "obf-999") {
		t.Fatalf("提示应带上引用号，实际 %s", bad.Body.String())
	}
	// 路径穿越：目录层的白名单要在建任务时就拦下
	badPath := create(map[string]string{"path": "/etc/../etc/passwd", "fileId": rec.Ref})
	if badPath.Code != http.StatusBadRequest {
		t.Fatalf("路径穿越应返回 400，实际 %d", badPath.Code)
	}
}

// 权限门控：上传要与下发同级（只读用户能上传就等于能把任意内容塞进平台），列举是只读的。
//
// 走真实路由（newRoutesMux）而不是直接调 handler：权限判定在路由的 permit 上，
// 直接调 handler 等于把要验的那一层绕过去了。
func TestRoutes_OpsFileUploadPermission(t *testing.T) {
	a, _ := opsTestAPI(t)
	mux := newRoutesMux(a)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "a.conf")
	_, _ = fw.Write([]byte("x"))
	_ = mw.Close()

	upload := func(p *auth.Principal) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/ops/files", bytes.NewReader(buf.Bytes()))
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req = req.WithContext(context.WithValue(req.Context(), principalContextKey{}, p))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	if got := upload(restrictedPrincipal([]string{"ops:read"}, "g1")); got.Code != http.StatusForbidden {
		t.Fatalf("只有 ops:read 时上传应 403，实际 %d", got.Code)
	}
	if got := upload(globalPrincipal("ops:exec")); got.Code != http.StatusCreated {
		t.Fatalf("有 ops:exec 应可上传，实际 %d（%s）", got.Code, got.Body.String())
	}

	// 列举只读即可，且必须给空数组而不是 null
	list := httptest.NewRecorder()
	mux.ServeHTTP(list, reqWith(restrictedPrincipal([]string{"ops:read"}, "g1"), http.MethodGet, "/api/v1/ops/files", ""))
	if list.Code != http.StatusOK {
		t.Fatalf("列举应放行，实际 %d", list.Code)
	}
	if !strings.Contains(list.Body.String(), `"files":[`) {
		t.Fatalf("列举应返回数组，实际 %s", list.Body.String())
	}
}
