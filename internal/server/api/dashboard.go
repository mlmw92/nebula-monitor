package api

import (
	"net/http"
	"os"
	"path/filepath"
)

// RegisterDashboard 将前端 SPA 注册到 mux 根路径（从磁盘 webDir 读取）。
// 改前端只需替换 webDir 下文件并重启 server，无需重新编译二进制。
func (a *API) RegisterDashboard(mux *http.ServeMux) {
	webDir := a.webDir
	if webDir == "" {
		webDir = "/etc/monitor-server/web"
	}
	indexFile := filepath.Join(webDir, "index.html")
	if _, err := os.Stat(indexFile); err != nil {
		mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "前端资源未找到（webDir="+webDir+"），请把 web/dist 拷贝到该目录", http.StatusInternalServerError)
		})
		return
	}
	fs := http.FileServer(http.Dir(webDir))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		// SPA：根路径或找不到的静态资源回退到 index.html
		if r.URL.Path == "/" {
			http.ServeFile(w, r, indexFile)
			return
		}
		fp := filepath.Join(webDir, filepath.Clean(r.URL.Path))
		if _, err := os.Stat(fp); err != nil {
			http.ServeFile(w, r, indexFile)
			return
		}
		fs.ServeHTTP(w, r)
	})
}
