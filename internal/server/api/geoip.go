package api

import (
	"io"
	"net/http"
	"strings"

	"github.com/nebula/monitor/internal/server/nginxaccess"
)

// IP 地理库文件最大 64 MB（ip2region v4 全量库约 11 MB）
const geoipMaxBytes = 64 << 20

// geoipResp 是 IP 地理库状态响应。
type geoipResp struct {
	nginxaccess.DBInfo
	Editable bool             `json:"editable"` // 是否配置了可写的存放路径（未配置时仅能用内置库）
	Samples  []geoipSampleRow `json:"samples"`  // 样本查询，便于确认库是否生效
}

// geoipSampleRow 是单条样本查询结果。
type geoipSampleRow struct {
	IP       string `json:"ip"`
	Country  string `json:"country"`
	Province string `json:"province"`
	City     string `json:"city"`
	Region   string `json:"region"` // 原始分段串
}

// geoipSampleIPs 用于展示库是否正常工作的固定样本。
var geoipSampleIPs = []string{"114.114.114.114", "223.5.5.5", "8.8.8.8"}

// handleGeoIPStatus 返回当前生效的 IP 地理库信息与样本查询结果。
// GET /api/v1/system/geoip
func (a *API) handleGeoIPStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, buildGeoIPResp())
}

// handleGeoIPUpload 上传 ip2region v4 xdb 文件替换 IP 地理库并立即热加载。
// POST /api/v1/system/geoip/upload  表单字段：file
//
// 该入口独立于系统升级：只替换地理库文件，不重启 server、不触碰 server/web/agent。
func (a *API) handleGeoIPUpload(w http.ResponseWriter, r *http.Request) {
	if nginxaccess.GeoOverridePath() == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "未配置 IP 地理库存放路径（server.yaml: geoipFile）"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, geoipMaxBytes)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "解析上传内容失败：" + err.Error()})
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少上传文件（表单字段 file）"})
		return
	}
	defer file.Close()
	if header != nil && header.Filename != "" && !strings.HasSuffix(strings.ToLower(header.Filename), ".xdb") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "仅支持 ip2region v4 的 .xdb 文件"})
		return
	}
	data, err := io.ReadAll(file)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "读取上传文件失败：" + err.Error()})
		return
	}
	if _, err := nginxaccess.ReplaceGeoDB(data); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, buildGeoIPResp())
}

// handleGeoIPReset 删除覆盖文件，回退到随程序内置的地理库。
// POST /api/v1/system/geoip/reset
func (a *API) handleGeoIPReset(w http.ResponseWriter, r *http.Request) {
	if _, err := nginxaccess.ResetGeoDB(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, buildGeoIPResp())
}

// handleGeoIPTest 用当前库查询指定 IP，便于更新后验证归属地。
// GET /api/v1/system/geoip/test?ip=1.2.3.4
func (a *API) handleGeoIPTest(w http.ResponseWriter, r *http.Request) {
	ip := strings.TrimSpace(r.URL.Query().Get("ip"))
	if ip == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少参数 ip"})
		return
	}
	region, err := nginxaccess.GeoSearchRaw(ip)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "查询失败：" + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, geoipSample(ip, region))
}

// buildGeoIPResp 组装状态响应。
func buildGeoIPResp() geoipResp {
	resp := geoipResp{
		DBInfo:   nginxaccess.GeoDBInfo(),
		Editable: nginxaccess.GeoOverridePath() != "",
	}
	for _, ip := range geoipSampleIPs {
		region, err := nginxaccess.GeoSearchRaw(ip)
		if err != nil {
			continue
		}
		resp.Samples = append(resp.Samples, geoipSample(ip, region))
	}
	return resp
}

// geoipSample 把原始 region 串拆成展示字段。
func geoipSample(ip, region string) geoipSampleRow {
	row := geoipSampleRow{IP: ip, Region: region}
	geo := nginxaccess.NewGeo()
	country, _, province, city := geo.Search(ip)
	row.Country, row.Province, row.City = country, province, city
	return row
}
