package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/server/config"
)

// 登录失败限流：每个源 IP 在窗口内最多允许 loginLimitMax 次失败，超出返回 429。
var (
	loginMu       sync.Mutex
	loginFails    = map[string]*loginAttempt{}
	loginLimitMax = 5
	loginLimitWin = 5 * time.Minute
)

type loginAttempt struct {
	count int
	since time.Time
}

func loginAllowed(ip string) bool {
	loginMu.Lock()
	defer loginMu.Unlock()
	now := time.Now()
	a, ok := loginFails[ip]
	if !ok || now.Sub(a.since) > loginLimitWin {
		loginFails[ip] = &loginAttempt{count: 0, since: now}
		return true
	}
	return a.count < loginLimitMax
}

func loginFail(ip string) {
	loginMu.Lock()
	defer loginMu.Unlock()
	if a, ok := loginFails[ip]; ok {
		a.count++
	} else {
		loginFails[ip] = &loginAttempt{count: 1, since: time.Now()}
	}
}

// clientIP 从 RemoteAddr 取出源 IP（去掉端口）。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// token 有效期
const tokenTTL = 24 * time.Hour

// 生成无状态 token：base64(username:exp) + "." + hmacSig
func genToken(username, secret string) string {
	exp := time.Now().Add(tokenTTL).Unix()
	payload := username + ":" + itoa(exp)
	sig := hmacSign(secret, payload)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + sig
}

// 校验 token，返回 username
func verifyToken(token, secret string) (string, bool) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return "", false
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", false
	}
	payload := string(payloadBytes)
	if !hmac.Equal([]byte(hmacSign(secret, payload)), []byte(parts[1])) {
		return "", false
	}
	idx := strings.LastIndex(payload, ":")
	if idx < 0 {
		return "", false
	}
	user := payload[:idx]
	exp, err := atoi(payload[idx+1:])
	if err != nil || time.Now().Unix() > exp {
		return "", false
	}
	return user, true
}

func hmacSign(secret, data string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(data))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// 公开路径（无需登录 token）
func isPublicPath(path string) bool {
	if path == "/" || strings.HasPrefix(path, "/assets/") {
		return true
	}
	// 登录、Agent 上报（Agent 走 X-Agent-Secret 校验，不走登录 token）
	if strings.HasPrefix(path, "/api/v1/login") || strings.HasPrefix(path, "/api/v1/report") {
		return true
	}
	if strings.HasPrefix(path, "/install/") || strings.HasPrefix(path, "/bin/") {
		return true
	}
	if strings.HasPrefix(path, "/ws") {
		return true
	}
	return false
}

// AuthMiddleware 校验 token；未启用 auth 则全放行
func AuthMiddleware(next http.Handler, authCfg config.AuthConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authCfg.Enabled {
			next.ServeHTTP(w, r)
			return
		}
		if isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		tok := r.Header.Get("Authorization")
		tok = strings.TrimPrefix(tok, "Bearer ")
		if tok == "" {
			if c, err := r.Cookie("nebula_token"); err == nil {
				tok = c.Value
			}
		}
		if _, ok := verifyToken(tok, authCfg.Secret); !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "未登录或登录已过期"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handleLogin POST /api/v1/login {username, password} -> {token}
func (a *API) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": "请求格式错误"})
		return
	}
	if !a.auth.Enabled {
		// 未启用认证，返回占位 token
		writeJSON(w, 200, map[string]interface{}{"token": "", "authEnabled": false})
		return
	}
	ip := clientIP(r)
	if !loginAllowed(ip) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "登录失败次数过多，请稍后再试"})
		return
	}
	userOK := subtle.ConstantTimeCompare([]byte(body.Username), []byte(a.auth.Username)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(body.Password), []byte(a.auth.Password)) == 1
	if !userOK || !passOK {
		loginFail(ip)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "用户名或密码错误"})
		return
	}
	tok := genToken(body.Username, a.auth.Secret)
	// 同时设置 cookie（方便浏览器直接访问）
	http.SetCookie(w, &http.Cookie{
		Name:     "nebula_token",
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		MaxAge:   int(tokenTTL.Seconds()),
	})
	writeJSON(w, 200, map[string]interface{}{"token": tok, "username": body.Username, "authEnabled": true})
}

// handleLogout 注销（前端清 token 即可，后端无状态）
func (a *API) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "nebula_token", Path: "/", MaxAge: -1})
	writeJSON(w, 200, map[string]string{"ok": "true"})
}

// handleAuthInfo 返回是否启用认证（前端用于决定是否显示登录页）
func (a *API) handleAuthInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]interface{}{"authEnabled": a.auth.Enabled})
}

// 简易 int 转 string（避免引入 strconv）
func itoa(i int64) string {
	return strings.TrimSpace(formatInt(i))
}

func atoi(s string) (int64, error) {
	var i int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errInvalid
		}
		i = i*10 + int64(c-'0')
	}
	return i, nil
}

var errInvalid = &invalidErr{}

type invalidErr struct{}

func (e *invalidErr) Error() string { return "invalid number" }

func formatInt(i int64) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
