package api

import (
	"log/slog"
	"net/http"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/config"
	"github.com/nebula/monitor/internal/server/nginxaccess"
)

// nginxAccessInstance 是大屏 Nginx 分析板块的实例级访问汇总。
type nginxAccessInstance struct {
	Instance          string  `json:"instance"`
	Node              string  `json:"node"`
	NodeIP            string  `json:"nodeIp"`
	Name              string  `json:"name"`
	Up                bool    `json:"up"`
	ActiveConnections float64 `json:"activeConnections"`
	Requests          float64 `json:"requests"`
	Bytes             float64 `json:"bytes"`
	ReqRate           float64 `json:"reqRate"`
}

// nginxAccessSummaryResp 是 /access/summary 的响应体。
type nginxAccessSummaryResp struct {
	TotalRequests float64            `json:"totalRequests"`
	TotalRate     float64            `json:"totalRate"`
	TotalBytes    float64            `json:"totalBytes"`
	StatusCounts  map[string]float64 `json:"statusCounts"`
	TopURIs       []model.NameCount  `json:"topUris"`
	TopIPs        []nginxaccess.IPAgg `json:"topIps"`
	Instances     []nginxAccessInstance `json:"instances"`
}

// nginxAccessLine 是地图动线的起终点对（中文名 + 英文名，坐标由前端按地图 name 解析）。
type nginxAccessLine struct {
	From   string  `json:"from"`
	FromEn string  `json:"fromEn"`
	To     string  `json:"to"`
	ToEn   string  `json:"toEn"`
	Value  float64 `json:"value"`
}

// nginxAccessGeoResp 是 /access/geo 的响应体。
type nginxAccessGeoResp struct {
	Scope        string              `json:"scope"`
	Points       []nginxaccess.Point `json:"points"`
	DeployPoints []nginxaccess.Point `json:"deployPoints"`
	Lines        []nginxAccessLine   `json:"lines"`
}

// handleNginxAccessSummary 返回 Nginx 访问量统计（窗口内汇总 + 实例级状态）。
func (a *API) handleNginxAccessSummary(w http.ResponseWriter, r *http.Request) {
	summary := nginxaccess.Summary{}
	if a.ngx != nil {
		summary = a.ngx.Summary()
	}
	resp := nginxAccessSummaryResp{
		TotalRequests: summary.TotalRequests,
		TotalRate:     summary.TotalRate,
		TotalBytes:    summary.TotalBytes,
		StatusCounts:  summary.StatusCounts,
		TopURIs:       summary.TopURIs,
		TopIPs:        summary.TopIPs,
	}

	// 实例级 up/连接数（复用 nginx_instance_up + nginx_active_connections）
	instances := map[string]*nginxAccessInstance{}
	var keys []string
	upSeries, err := a.store.QueryAllLatest("nginx_instance_up", nil)
	if err != nil {
		slog.Warn("查询 Nginx 实例失败", "err", err)
	} else {
		for _, s := range upSeries {
			node := s.Labels["node"]
			instance := s.Labels["instance"]
			if node == "" || instance == "" || len(s.Points) == 0 {
				continue
			}
			key := node + "|" + instance
			if _, ok := instances[key]; !ok {
				instances[key] = &nginxAccessInstance{
					Instance: instance,
					Node:     node,
					NodeIP:   s.Labels["node_ip"],
					Name:     s.Labels["name"],
					Up:       s.Points[len(s.Points)-1].Value > 0,
				}
				keys = append(keys, key)
			}
		}
	}
	connSeries, err := a.store.QueryAllLatest("nginx_active_connections", nil)
	if err == nil {
		for _, s := range connSeries {
			key := s.Labels["node"] + "|" + s.Labels["instance"]
			if ri, ok := instances[key]; ok && len(s.Points) > 0 {
				ri.ActiveConnections = round2(s.Points[len(s.Points)-1].Value)
			}
		}
	}
	// 合并窗口内实例速率
	for inst, ir := range summary.Instances {
		for _, key := range keys {
			ri := instances[key]
			if ri.Instance == inst {
				ri.Requests = ir.Requests
				ri.Bytes = ir.Bytes
				ri.ReqRate = ir.Rate
				break
			}
		}
	}
	resp.Instances = make([]nginxAccessInstance, 0, len(keys))
	for _, key := range keys {
		resp.Instances = append(resp.Instances, *instances[key])
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleNginxAccessGeo 返回请求来源地理分布（热力点 + 部署点 + 动线）。
// scope=cn 中国省份地图；scope=world 世界国家地图。
func (a *API) handleNginxAccessGeo(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")
	if scope != "world" {
		scope = "cn"
	}
	resp := nginxAccessGeoResp{Scope: scope}
	if a.ngx == nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.Points = a.ngx.Points(scope)
	// 部署点：服务器（数据中心）所在地，取大屏配置的省级行政区；
	// 世界地图上省份无对应区域，统一落到国家中心。
	deployName := config.DefaultDeployLocation
	if a.screenMgr != nil {
		if loc := a.screenMgr.Get().DeployLocation; config.IsValidDeployLocation(loc) {
			deployName = loc
		}
	}
	center := nginxaccess.Point{Name: deployName}
	if scope == "world" {
		deployName = "中国"
		center = nginxaccess.Point{Name: deployName, CountryEn: "China"}
	}
	for _, p := range resp.Points {
		if p.Name == deployName {
			center.Requests += p.Requests
			center.Bytes += p.Bytes
		}
	}
	resp.DeployPoints = []nginxaccess.Point{center}
	// 动线：各来源点 → 部署点
	for _, p := range resp.Points {
		if p.Name == deployName {
			continue
		}
		resp.Lines = append(resp.Lines, nginxAccessLine{
			From: p.Name, FromEn: p.CountryEn,
			To: deployName, ToEn: center.CountryEn,
			Value: p.Requests,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}
