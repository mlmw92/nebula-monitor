package api

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nebula/monitor/internal/server/auth"
	"github.com/nebula/monitor/internal/server/config"
)

// TestGenVerifyToken_RoundTrip 合法 token 往返：用户名与 tokenVersion 必须保真。
func TestGenVerifyToken_RoundTrip(t *testing.T) {
	tok := genToken("alice", 7, "s3cret")
	user, tv, ok := verifyToken(tok, "s3cret")
	if !ok {
		t.Fatal("合法 token 应校验通过")
	}
	if user != "alice" || tv != 7 {
		t.Fatalf("解析结果 = (%q,%d)，want (alice,7)", user, tv)
	}
	// payload 应为 base64url(username:tokenVersion:exp)，且 exp 在有效期内
	payload, err := base64.RawURLEncoding.DecodeString(strings.SplitN(tok, ".", 2)[0])
	if err != nil {
		t.Fatalf("payload 非 base64url: %v", err)
	}
	if n := len(strings.Split(string(payload), ":")); n != 3 {
		t.Fatalf("payload 段数 = %d，want 3：%s", n, payload)
	}
}

// TestVerifyToken_RejectsTamperedToken 密钥不符、载荷/签名被改、结构非法都要拒绝。
func TestVerifyToken_RejectsTamperedToken(t *testing.T) {
	tok := genToken("alice", 0, "s3cret")
	parts := strings.SplitN(tok, ".", 2)

	// 1) 换了密钥
	if _, _, ok := verifyToken(tok, "other-secret"); ok {
		t.Error("密钥不符应失败")
	}

	// 2) 篡改载荷但沿用旧签名（提权为 bob）
	payload := "bob:0:" + itoa(time.Now().Add(time.Hour).Unix())
	forged := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + parts[1]
	if _, _, ok := verifyToken(forged, "s3cret"); ok {
		t.Error("篡改载荷应失败")
	}

	// 3) 篡改签名
	if _, _, ok := verifyToken(parts[0]+"."+strings.Repeat("A", len(parts[1])), "s3cret"); ok {
		t.Error("篡改签名应失败")
	}

	// 4) 结构非法
	for _, bad := range []string{"", "nodot", "a.b.c", "!!!." + parts[1]} {
		if _, _, ok := verifyToken(bad, "s3cret"); ok {
			t.Errorf("非法 token %q 应失败", bad)
		}
	}
}

// TestVerifyToken_RejectsExpired 过期 token（签名正确）必须失败。
func TestVerifyToken_RejectsExpired(t *testing.T) {
	payload := "alice:0:" + itoa(time.Now().Add(-time.Minute).Unix())
	expired := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + hmacSign("s3cret", payload)
	if _, _, ok := verifyToken(expired, "s3cret"); ok {
		t.Fatal("过期 token 应失败")
	}
}

// TestVerifyToken_RejectsMalformedPayload 签名正确但载荷结构/数字非法时失败。
func TestVerifyToken_RejectsMalformedPayload(t *testing.T) {
	for _, payload := range []string{
		"alice",            // 无分隔符
		"alice:",           // 只有一段
		"alice:x:12345678", // tokenVersion 非数字
		"alice:0:notanumber",
	} {
		tok := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + hmacSign("s3cret", payload)
		if _, _, ok := verifyToken(tok, "s3cret"); ok {
			t.Errorf("载荷 %q 应失败", payload)
		}
	}
}

// TestAuthMiddleware_TokenPaths 覆盖中间件的 token 来源与各条 401 分支：
// 无凭据、Bearer、Cookie、tokenVersion 失效（改密/禁用后旧会话）、用户不存在、公开路径。
func TestAuthMiddleware_TokenPaths(t *testing.T) {
	store, err := auth.NewStore(filepath.Join(t.TempDir(), "users.yaml"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.CreateUser(auth.User{
		Username: "alice", Roles: []string{auth.RoleReadOnly},
		Scope: auth.Scope{Mode: auth.ScopeGlobal}, Status: auth.StatusEnabled,
	}, "Passw0rd!", "admin"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	p := store.GetPrincipal("alice")
	if p == nil {
		t.Fatal("应能取到 alice 的授权身份")
	}

	cfg := config.AuthConfig{Enabled: true, Secret: "s3cret"}
	h := AuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), cfg, store)

	do := func(req *http.Request) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	newReq := func() *http.Request { return httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil) }

	if code := do(newReq()); code != http.StatusUnauthorized {
		t.Fatalf("无凭据应 401，code = %d", code)
	}

	req := newReq()
	req.Header.Set("Authorization", "Bearer "+genToken("alice", p.TokenVersion, cfg.Secret))
	if code := do(req); code != http.StatusOK {
		t.Fatalf("Bearer 合法 token 应放行，code = %d", code)
	}

	// Cookie 兜底：浏览器 WebSocket 握手无法设置请求头，依赖这条路径
	req = newReq()
	req.AddCookie(&http.Cookie{Name: "nebula_token", Value: genToken("alice", p.TokenVersion, cfg.Secret)})
	if code := do(req); code != http.StatusOK {
		t.Fatalf("Cookie 合法 token 应放行，code = %d", code)
	}

	// 会话失效：tokenVersion 与存储不一致（改密/禁用后旧 token）
	req = newReq()
	req.Header.Set("Authorization", "Bearer "+genToken("alice", p.TokenVersion+1, cfg.Secret))
	if code := do(req); code != http.StatusUnauthorized {
		t.Fatalf("tokenVersion 不一致应 401，code = %d", code)
	}

	// 用户不存在
	req = newReq()
	req.Header.Set("Authorization", "Bearer "+genToken("ghost", 0, cfg.Secret))
	if code := do(req); code != http.StatusUnauthorized {
		t.Fatalf("用户不存在应 401，code = %d", code)
	}

	// 公开路径（品牌配置匿名只读）无需 token
	if code := do(httptest.NewRequest(http.MethodGet, "/api/v1/ui/settings", nil)); code != http.StatusOK {
		t.Fatalf("公开路径应放行，code = %d", code)
	}
}

// TestAuthMiddleware_DisabledAllowsAll 未启用认证时中间件整体放行（兼容策略 1）。
func TestAuthMiddleware_DisabledAllowsAll(t *testing.T) {
	h := AuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), config.AuthConfig{Enabled: false}, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("未启用认证应放行，code = %d", rec.Code)
	}
}
