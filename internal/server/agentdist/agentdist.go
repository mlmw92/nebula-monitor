// Package agentdist serves the agent binary and install script to agents.
package agentdist

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/nebula/monitor/internal/server/config"
)

// Distributor serves agent binaries and install scripts.
type Distributor struct {
	BinDir    string
	ScriptDir string
	AgentAuth config.AgentAuthConfig
}

// New creates a Distributor. When AgentAuth is enabled, downloading the agent
// binary requires a valid X-Agent-Secret header.
func New(binDir, scriptDir string, agentAuth config.AgentAuthConfig) *Distributor {
	return &Distributor{BinDir: binDir, ScriptDir: scriptDir, AgentAuth: agentAuth}
}

// Register installs the distribution routes on mux.
func (d *Distributor) Register(mux *http.ServeMux) {
	// Install script stays public so new agents can bootstrap.
	mux.HandleFunc("GET /install/agent-install.sh", d.serveInstallScript)
	// Agent binary and its checksum are protected when agent auth is enabled.
	mux.HandleFunc("GET /bin/", d.serveBin)
}

// checksumResponse is returned by the .sha256 endpoint.
type checksumResponse struct {
	Checksum string `json:"checksum"`
	Sig      string `json:"sig"`
}

func (d *Distributor) serveInstallScript(w http.ResponseWriter, r *http.Request) {
	path := d.ScriptDir
	// AgentScriptPath 可能是脚本文件路径，也可能是目录；两种都兼容。
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		path = filepath.Join(path, "agent-install.sh")
	}
	d.serveFile(w, r, path)
}

// serveBin serves a path under /bin/. Binary downloads (/bin/linux/<arch>/agent)
// are protected when agent auth is enabled; the matching .sha256 checksum is
// always served but is useless without the HMAC signature key.
func (d *Distributor) serveBin(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/bin/")
	clean := filepath.Clean(rel)
	if strings.HasPrefix(clean, "..") || strings.Contains(clean, "..") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// 目标二进制（或 .sha256 对应的二进制）路径。
	// apply 把 agent 写到 BinDir/agent/linux/<arch>/agent；
	// 历史 agent 升级 URL 为 /bin/<arch>/agent（无 linux 段），新 agent 为 /bin/linux/<arch>/agent。
	// 这里按候选顺序兼容多种结构，确保新旧 agent 都能下到正确二进制。
	binName := strings.TrimSuffix(clean, ".sha256")
	candidates := []string{
		filepath.Join(d.BinDir, binName),
		filepath.Join(d.BinDir, "agent", binName),
		filepath.Join(d.BinDir, "agent", "linux", binName),
		filepath.Join(d.BinDir, "linux", binName),
	}
	target := ""
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			target = c
			break
		}
	}
	if target == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// The agent binary itself requires authorization when agent auth is on.
	if strings.HasSuffix(clean, "/agent") || filepath.Base(clean) == "agent" {
		if d.AgentAuth.Enabled && !d.validSecret(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	if strings.HasSuffix(clean, ".sha256") {
		d.serveChecksum(w, r, target)
		return
	}

	d.serveFile(w, r, target)
}

// serveChecksum computes the SHA256 of the agent binary and signs it with the
// agent auth secret (HMAC-SHA256). The signature lets agents verify integrity
// even over plaintext HTTP, provided they share the secret.
func (d *Distributor) serveChecksum(w http.ResponseWriter, r *http.Request, binPath string) {
	data, err := os.ReadFile(binPath)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	sum := sha256.Sum256(data)
	checksum := hex.EncodeToString(sum[:])

	resp := checksumResponse{Checksum: checksum}
	if d.AgentAuth.Enabled && d.AgentAuth.Secret != "" {
		mac := hmac.New(sha256.New, []byte(d.AgentAuth.Secret))
		mac.Write([]byte(checksum))
		resp.Sig = hex.EncodeToString(mac.Sum(nil))
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(resp)
}

func (d *Distributor) serveFile(w http.ResponseWriter, r *http.Request, path string) {
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	http.ServeContent(w, r, filepath.Base(path), info.ModTime(), f)
}

// validSecret checks the X-Agent-Secret header in constant time.
func (d *Distributor) validSecret(r *http.Request) bool {
	got := r.Header.Get("X-Agent-Secret")
	if got == "" {
		return false
	}
	want := d.AgentAuth.Secret
	if want == "" {
		return false
	}
	return hmac.Equal([]byte(got), []byte(want))
}
