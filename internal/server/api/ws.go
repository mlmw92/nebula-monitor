package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/storage"
)

// checkWSLSameOrigin 仅允许同源（或缺少 Origin 头的非浏览器客户端）建立 WebSocket，
// 避免任意第三方站点跨域订阅实时监控数据（之前恒返回 true）。
func checkWSLSameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // 同源请求或非浏览器客户端通常不带 Origin
	}
	ou, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return ou.Host == r.Host
}

// aggregateNetworkMetric 将按接口拆标签上报的网络指标（速率/累计）跨接口求和，
// 返回一个聚合点；用于 WS 实时推送，保证前端拿到的是整机总值（而非某一接口）。
func aggregateNetworkMetric(store storage.Storage, node, name string) (*model.Point, error) {
	series, err := store.QueryInstant(node, name, nil)
	if err != nil {
		return nil, err
	}
	var sum float64
	var ts int64
	for _, s := range series {
		if len(s.Points) == 0 {
			continue
		}
		p := s.Points[len(s.Points)-1]
		sum += p.Value
		if p.Timestamp > ts {
			ts = p.Timestamp
		}
	}
	if ts == 0 {
		return nil, nil
	}
	return &model.Point{Timestamp: ts, Value: sum}, nil
}

// upgrader 将 HTTP 连接升级为 WebSocket 的 Upgrader，校验同源。
var upgrader = websocket.Upgrader{
	CheckOrigin: checkWSLSameOrigin,
}

// Hub 管理 WebSocket 客户端连接，并广播告警事件。
type Hub struct {
	mu      sync.Mutex
	clients map[*Client]bool
	alertCh chan model.AlertEvent
}

// Client 表示一个 WS 客户端。
type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
}

// NewHub 创建 Hub。
func NewHub() *Hub {
	return &Hub{
		clients: map[*Client]bool{},
		alertCh: make(chan model.AlertEvent, 64),
	}
}

// Run 启动 Hub 事件循环（广播告警）。
func (h *Hub) Run() {
	for e := range h.alertCh {
		b, err := json.Marshal(map[string]interface{}{"type": "alert", "data": e})
		if err != nil {
			continue
		}
		h.mu.Lock()
		for c := range h.clients {
			select {
			case c.send <- b:
			default:
			}
		}
		h.mu.Unlock()
	}
}

// Register 注册客户端。
func (h *Hub) Register(c *Client) {
	h.mu.Lock()
	h.clients[c] = true
	h.mu.Unlock()
}

// Unregister 注销客户端。
func (h *Hub) Unregister(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c.send)
	}
}

// ClientCount 返回当前已注册的 WebSocket 客户端数量（自监控用）。
func (h *Hub) ClientCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

// BroadcastAlert 广播告警事件给所有 WS 客户端。
func (h *Hub) BroadcastAlert(e model.AlertEvent) {
	select {
	case h.alertCh <- e:
	default:
		slog.Warn("告警广播队列已满，丢弃", "node", e.Node)
	}
}

// RegisterWS 注册带授权的 WebSocket 端点 /ws。
// 查询参数：topic=metrics&node=<name> 推送节点实时指标；topic=alerts 接收告警广播。
//
// 授权在握手前完成（由 API 包装而非 Hub 自身），因为范围校验需要节点与分组信息：
//   - topic=metrics：需 nodes:read，且 node 必须落在当前用户的资源范围内；
//   - topic=alerts：需 alerts:read；
//   - 其它 topic：直接拒绝，避免出现「登录即可订阅任意数据」的越权面。
func (a *API) RegisterWS(mux *http.ServeMux, store storage.Storage) {
	mux.HandleFunc("GET /ws", a.wsAuthorize(func(w http.ResponseWriter, r *http.Request) {
		a.hub.handleWS(store, w, r)
	}))
}

// wsAuthorize 校验 WebSocket 订阅授权（topic 级权限点 + 节点资源范围）。
func (a *API) wsAuthorize(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		topic := r.URL.Query().Get("topic")
		var perm string
		switch topic {
		case "metrics":
			perm = "nodes:read"
		case "alerts":
			perm = "alerts:read"
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "未知的 topic 参数"})
			return
		}
		if !a.checkPerm(w, r, perm) {
			return
		}
		if topic == "metrics" {
			node := r.URL.Query().Get("node")
			if node == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "topic=metrics 需要 node 参数"})
				return
			}
			if !a.checkNodeScope(w, r, perm, node) {
				return
			}
		}
		next(w, r)
	}
}

// handleWS 处理 WebSocket 连接。
// topic=metrics&node=<name> 推送该节点实时指标（轮询 VM 最新点）；topic=alerts 接收告警广播。
func (h *Hub) handleWS(store storage.Storage, w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		// 握手失败：常见原因
		//  1) CheckOrigin 拒绝（Origin 与 Host 不一致，多为反代未透传 Host / 跨域）；
		//  2) 前置认证中间件已返回 401/403（会话过期或未登录）。
		// 记录日志便于定位，避免仅靠浏览器侧 “closed before established” 难懂报错。
		slog.Warn("WebSocket 握手失败",
			"remote", r.RemoteAddr,
			"host", r.Host,
			"origin", r.Header.Get("Origin"),
			"topic", r.URL.Query().Get("topic"),
			"err", err.Error())
		return
	}
	client := &Client{hub: h, conn: conn, send: make(chan []byte, 16)}
	h.Register(client)

	topic := r.URL.Query().Get("topic")
	node := r.URL.Query().Get("node")

	go client.writePump()
	go client.readPump()

	if topic == "metrics" && node != "" {
		go h.pushNodeMetrics(client, store, node)
	}
}

// pushNodeMetrics 每 1s 查询 VM 最新指标并推送给客户端。
func (h *Hub) pushNodeMetrics(client *Client, store storage.Storage, node string) {
	metrics := []string{"cpu_usage", "mem_used_percent", "disk_used_percent",
		"swap_used_percent", "network_recv_rate", "network_sent_rate",
		"network_recv_total", "network_sent_total",
		"load1", "load5", "load15", "disk_read_rate", "disk_write_rate"}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case _, ok := <-client.send:
			// writePump 在连接关闭时已关闭 send，读到关闭值即退出
			if !ok {
				return
			}
		case <-ticker.C:
			var payload []map[string]interface{}
			for _, name := range metrics {
				switch {
				case name == "disk_used_percent":
					p, err := aggregateDiskUsageForNode(store, node)
					if err != nil || p == nil {
						continue
					}
					payload = append(payload, map[string]interface{}{
						"name": name, "value": p.Value, "timestamp": p.Timestamp,
					})
				case name == "network_recv_rate" || name == "network_sent_rate" ||
					name == "network_recv_total" || name == "network_sent_total":
					// 网络指标按接口拆标签上报，需跨接口汇总为单值后再推送
					p, err := aggregateNetworkMetric(store, node, name)
					if err != nil || p == nil {
						continue
					}
					payload = append(payload, map[string]interface{}{
						"name": name, "value": p.Value, "timestamp": p.Timestamp,
					})
				default:
					p, err := store.QueryLatest(node, name, nil)
					if err != nil || p == nil {
						continue
					}
					payload = append(payload, map[string]interface{}{
						"name": name, "value": p.Value, "timestamp": p.Timestamp,
					})
				}
			}
			if len(payload) > 0 {
				b, _ := json.Marshal(map[string]interface{}{"type": "metrics", "node": node, "data": payload})
				select {
				case client.send <- b:
				default:
				}
			}
		}
	}
}

// writePump 发送循环。
func (c *Client) writePump() {
	for msg := range c.send {
		if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			return
		}
	}
}

// readPump 读循环，检测断开后注销（关闭 send 以通知 pushNodeMetrics 退出）。
func (c *Client) readPump() {
	defer c.hub.Unregister(c)
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
	}
}
