package nginxaccess

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/model"
)

const (
	// maxTopURIs 汇总中保留的 Top URI 数量。
	maxTopURIs = 10
	// maxTopIPs 汇总中保留的 Top IP 数量。
	maxTopIPs = 50
)

// Point 地理来源点聚合（省份或国家）。
type Point struct {
	Name      string  `json:"name"`      // 省份简称 / 国家中文名
	CountryEn string  `json:"countryEn"` // 国家英文名（世界地图 GeoJSON name 匹配用）
	Requests  float64 `json:"requests"`  // 请求数
	Bytes     float64 `json:"bytes"`     // 响应字节数
}

// IPAgg 来源 IP 聚合（带归属地，用于 Top IP 排行）。
type IPAgg struct {
	IP        string  `json:"ip"`
	Requests  float64 `json:"requests"`
	Bytes     float64 `json:"bytes"`
	Country   string  `json:"country"`
	CountryEn string  `json:"countryEn"`
	Province  string  `json:"province"`
}

// InstanceRate 单实例汇总（用于实例速率展示）。
type InstanceRate struct {
	Requests float64 `json:"requests"`
	Bytes    float64 `json:"bytes"`
	Rate     float64 `json:"rate"` // 每秒请求数
}

// InstanceSummary 是单个 Nginx 实例的访问分析汇总。
type InstanceSummary struct {
	TotalRequests float64            `json:"totalRequests"`
	TotalRate     float64            `json:"totalRate"`
	TotalBytes    float64            `json:"totalBytes"`
	StatusCounts  map[string]float64 `json:"statusCounts"`
	TopURIs       []model.NameCount  `json:"topUris"`
	TopIPs        []IPAgg            `json:"topIps"`
}

// Summary 是 Nginx 访问汇总快照。
type Summary struct {
	TotalRequests     float64                    `json:"totalRequests"`
	TotalBytes        float64                    `json:"totalBytes"`
	TotalRate         float64                    `json:"totalRate"` // 每秒请求数
	StatusCounts      map[string]float64         `json:"statusCounts"`
	TopURIs           []model.NameCount          `json:"topUris"`
	TopIPs            []IPAgg                    `json:"topIps"`
	Instances         map[string]InstanceRate    `json:"instances"`         // key=instance
	InstanceSummaries map[string]InstanceSummary `json:"instanceSummaries"` // key=instance
}

// instanceAgg 单实例周期聚合。
type instanceAgg struct {
	requests     float64
	bytes        float64
	periodSecSum float64
	statusCnt    map[string]float64
	uris         map[string]float64
	ips          map[string]*IPAgg
	points       map[string]*Point
}

func accessInstanceKey(st model.NginxAccessStat) string {
	if st.Node != "" {
		return st.Node + "|" + st.Instance
	}
	return st.Instance
}

// Window 内存滑动窗口聚合 Nginx access log（TTL 过期整体重置）。
// 实时大屏场景，数据保留最近一个窗口期，重启丢历史可接受。
type Window struct {
	mu  sync.RWMutex
	geo *Geo
	ttl time.Duration

	points        map[string]*Point       // key = scope|name
	statusCnt     map[string]float64      // status code → count
	uris          map[string]float64      // uri → count
	ips           map[string]*IPAgg       // ip → agg
	insts         map[string]*instanceAgg // instance → agg
	totalRequests float64
	totalBytes    float64
	periodSecSum  float64
	lastAddAt     int64
}

// NewWindow 创建滑动窗口。
func NewWindow(geo *Geo, ttl time.Duration) *Window {
	return &Window{
		geo:       geo,
		ttl:       ttl,
		points:    make(map[string]*Point),
		statusCnt: make(map[string]float64),
		uris:      make(map[string]float64),
		ips:       make(map[string]*IPAgg),
		insts:     make(map[string]*instanceAgg),
	}
}

// Add 接收一批上报的 access log 聚合统计。
func (w *Window) Add(stats []model.NginxAccessStat) {
	if len(stats) == 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	now := model.NowMillis()
	if w.lastAddAt == 0 {
		w.lastAddAt = now
	}
	// 滑动窗口：超过 TTL 整体重置
	if now-w.lastAddAt > int64(w.ttl/time.Millisecond) {
		w.reset()
		w.lastAddAt = now
	}
	for _, st := range stats {
		key := accessInstanceKey(st)
		ia := w.insts[key]
		if ia == nil {
			ia = &instanceAgg{}
			ia.statusCnt = make(map[string]float64)
			ia.uris = make(map[string]float64)
			ia.ips = make(map[string]*IPAgg)
			ia.points = make(map[string]*Point)
			w.insts[key] = ia
		}
		ia.requests += st.Requests
		ia.bytes += st.Bytes
		ia.periodSecSum += st.PeriodSec

		w.totalRequests += st.Requests
		w.totalBytes += st.Bytes
		w.periodSecSum += st.PeriodSec

		for code, cnt := range st.StatusCount {
			w.statusCnt[code] += cnt
			ia.statusCnt[code] += cnt
		}
		for _, u := range st.TopURIs {
			w.uris[u.Name] += u.Count
			ia.uris[u.Name] += u.Count
		}
		for _, ip := range st.TopIPs {
			agg := w.ips[ip.IP]
			if agg == nil {
				agg = &IPAgg{IP: ip.IP}
				agg.Country, agg.CountryEn, agg.Province, _ = w.geo.Search(ip.IP)
				w.ips[ip.IP] = agg
			}
			instAgg := ia.ips[ip.IP]
			if instAgg == nil {
				instAgg = &IPAgg{IP: ip.IP, Country: agg.Country, CountryEn: agg.CountryEn, Province: agg.Province}
				ia.ips[ip.IP] = instAgg
			}
			agg.Requests += ip.Requests
			agg.Bytes += ip.Bytes
			instAgg.Requests += ip.Requests
			instAgg.Bytes += ip.Bytes
			// 仅国内 IP（国家=中国）才计入中国省份地图，
			// 避免外国 IP 把城市名（如 Dronten）误当作省份聚合进来。
			if agg.Country == "中国" && agg.Province != "" {
				p := w.point("cn|"+agg.Province, "")
				p.Requests += ip.Requests
				p.Bytes += ip.Bytes
				p = instancePoint(ia.points, "cn|"+agg.Province, "")
				p.Requests += ip.Requests
				p.Bytes += ip.Bytes
			}
			if agg.Country != "" {
				p := w.point("world|"+agg.Country, agg.CountryEn)
				p.Requests += ip.Requests
				p.Bytes += ip.Bytes
				p = instancePoint(ia.points, "world|"+agg.Country, agg.CountryEn)
				p.Requests += ip.Requests
				p.Bytes += ip.Bytes
			}
		}
	}
}

// point 获取（或创建）指定 key 的地理点；countryEn 仅在创建时生效。
func (w *Window) point(key, countryEn string) *Point {
	return instancePoint(w.points, key, countryEn)
}

func instancePoint(points map[string]*Point, key, countryEn string) *Point {
	p := points[key]
	if p == nil {
		name := key[strings.Index(key, "|")+1:]
		p = &Point{Name: name, CountryEn: countryEn}
		points[key] = p
	}
	return p
}

// reset 清空窗口。
func (w *Window) reset() {
	w.points = make(map[string]*Point)
	w.statusCnt = make(map[string]float64)
	w.uris = make(map[string]float64)
	w.ips = make(map[string]*IPAgg)
	w.insts = make(map[string]*instanceAgg)
	w.totalRequests = 0
	w.totalBytes = 0
	w.periodSecSum = 0
}

// Points 返回指定 scope（cn|world）的来源点，按请求数降序。
func (w *Window) Points(scope string) []Point {
	return w.PointsFor(scope, "")
}

// PointsFor 返回指定实例的地理来源点；instance 为空时返回全局汇总。
func (w *Window) PointsFor(scope, instance string) []Point {
	w.mu.RLock()
	defer w.mu.RUnlock()
	points := w.points
	if instance != "" {
		if ia := w.insts[instance]; ia != nil {
			points = ia.points
		} else {
			return nil
		}
	}
	prefix := scope + "|"
	var out []Point
	for k, p := range points {
		if strings.HasPrefix(k, prefix) {
			out = append(out, *p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Requests > out[j].Requests })
	return out
}

func summarizeInstance(ia *instanceAgg) InstanceSummary {
	s := InstanceSummary{StatusCounts: make(map[string]float64)}
	if ia == nil {
		return s
	}
	s.TotalRequests, s.TotalBytes = ia.requests, ia.bytes
	if ia.periodSecSum > 0 {
		s.TotalRate = round2(ia.requests / ia.periodSecSum)
	}
	for code, count := range ia.statusCnt {
		s.StatusCounts[code] = count
	}
	for name, count := range ia.uris {
		s.TopURIs = append(s.TopURIs, model.NameCount{Name: name, Count: count})
	}
	sort.Slice(s.TopURIs, func(i, j int) bool { return s.TopURIs[i].Count > s.TopURIs[j].Count })
	if len(s.TopURIs) > maxTopURIs {
		s.TopURIs = s.TopURIs[:maxTopURIs]
	}
	for _, ip := range ia.ips {
		s.TopIPs = append(s.TopIPs, *ip)
	}
	sort.Slice(s.TopIPs, func(i, j int) bool { return s.TopIPs[i].Requests > s.TopIPs[j].Requests })
	if len(s.TopIPs) > maxTopIPs {
		s.TopIPs = s.TopIPs[:maxTopIPs]
	}
	return s
}

// Summary 返回访问汇总快照。
func (w *Window) Summary() Summary {
	w.mu.RLock()
	defer w.mu.RUnlock()
	s := Summary{
		TotalRequests:     w.totalRequests,
		TotalBytes:        w.totalBytes,
		StatusCounts:      make(map[string]float64),
		Instances:         make(map[string]InstanceRate, len(w.insts)),
		InstanceSummaries: make(map[string]InstanceSummary, len(w.insts)),
	}
	if w.periodSecSum > 0 {
		s.TotalRate = round2(w.totalRequests / w.periodSecSum)
	}
	for c, n := range w.statusCnt {
		s.StatusCounts[c] = n
	}
	for u, c := range w.uris {
		s.TopURIs = append(s.TopURIs, model.NameCount{Name: u, Count: c})
	}
	sort.Slice(s.TopURIs, func(i, j int) bool { return s.TopURIs[i].Count > s.TopURIs[j].Count })
	if len(s.TopURIs) > maxTopURIs {
		s.TopURIs = s.TopURIs[:maxTopURIs]
	}
	for _, a := range w.ips {
		s.TopIPs = append(s.TopIPs, *a)
	}
	sort.Slice(s.TopIPs, func(i, j int) bool { return s.TopIPs[i].Requests > s.TopIPs[j].Requests })
	if len(s.TopIPs) > maxTopIPs {
		s.TopIPs = s.TopIPs[:maxTopIPs]
	}
	for inst, ia := range w.insts {
		rate := 0.0
		if ia.periodSecSum > 0 {
			rate = ia.requests / ia.periodSecSum
		}
		s.Instances[inst] = InstanceRate{Requests: ia.requests, Bytes: ia.bytes, Rate: round2(rate)}
		s.InstanceSummaries[inst] = summarizeInstance(ia)
	}
	return s
}

// round2 保留两位小数。
func round2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}
