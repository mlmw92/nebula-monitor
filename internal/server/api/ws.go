package api

import (
	"context"
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

// defaultWSWriteWait 是单次 WebSocket 写的默认截止时间。客户端「只连不读」时 TCP
// 接收窗口会归零，阻塞的写会让 writePump 永远挂住，于是收敛链（写错误 → 关闭连接
// → readPump 退出 → Unregister → pushNodeMetrics 退出）永不触发，连接与推送协程
// 双双泄漏。每次写前设置连接级 deadline，把「无限阻塞」变成「有界错误」。
// 注意：这是连接级 deadline，不是 http.Server.WriteTimeout——后者会对所有长连接生效。
// 值按连接存放在 Client.writeWait（此后不再变更），避免共享可变全局被并发读写。
const defaultWSWriteWait = 10 * time.Second

// defaultWSPingInterval 是服务端 ping 心跳间隔。若节点查询不到实时数据（TSDB 异常/
// 主机未上报），pushNodeMetrics 一帧都不推，连接处于静默空闲——nginx/LB 的空闲超时
// （如 proxy_read_timeout 默认 60s）会把它掐掉，前端随即进入 3s 重连循环，表现为
// 「WS 不断重新发起请求 + 页面永远等待实时数据」。无论是否有业务数据，按此间隔发
// ping 保活链路；浏览器会自动回 pong。取值须显著小于常见反代默认空闲超时。
const defaultWSPingInterval = 20 * time.Second

// defaultWSPongWait 是读侧 pong 等待上限：超过该时长未收到任何 pong/数据帧，
// 判定链路死亡并注销客户端。须大于 defaultWSPingInterval 的若干倍以容忍丢包。
const defaultWSPongWait = 60 * time.Second

// Hub 管理 WebSocket 客户端连接，并广播告警事件。
//
// 停机安全约定：
//   - 所有会向 client.send 写入的路径（Run 广播、pushNodeMetrics）都必须在 h.mu 内
//     确认客户端仍在 clients 中，再写入；关闭 channel 也在 h.mu 内完成，
//     因此不会出现「向已关闭 channel 发送」的 panic。
//   - CloseClients 幂等，只关闭各客户端的 send 与底层连接，不关闭 alertCh——
//     告警引擎仍可能短暂调用 BroadcastAlert。
type Hub struct {
	mu      sync.Mutex
	clients map[*Client]bool
	alertCh chan model.AlertEvent

	// done 在 CloseClients 首次调用时关闭，通知推送/广播协程停止。
	done     chan struct{}
	stopOnce sync.Once
}

// Client 表示一个 WS 客户端。
type Client struct {
	hub  *Hub
	conn *websocket.Conn
	// send 是唯一发送队列：仅写 send，由 writePump 消费。
	send chan []byte
	// done 与 send 在同一临界区内恰好关闭一次，通知 pushNodeMetrics 退出，
	// 避免客户端断线后 ticker 仍按秒查询 TSDB。
	done chan struct{}
	// writeDone 由 writePump 退出时关闭，供测试等待「唯一写者已退出」后再探测
	// 连接状态（gorilla 的 Conn 只允许一个并发写者）。
	writeDone chan struct{}
	// writeWait 是单次写的截止时间（构造时确定，之后只读）。
	writeWait time.Duration
}

// NewHub 创建 Hub。
func NewHub() *Hub {
	return &Hub{
		clients: map[*Client]bool{},
		alertCh: make(chan model.AlertEvent, 64),
		done:    make(chan struct{}),
	}
}

// newWSClient 构造已就绪的客户端（send / done 必须都在 Unregister 前建立）。
func newWSClient(h *Hub, conn *websocket.Conn) *Client {
	return &Client{
		hub:       h,
		conn:      conn,
		send:      make(chan []byte, 16),
		done:      make(chan struct{}),
		writeDone: make(chan struct{}),
		writeWait: defaultWSWriteWait,
	}
}

// Run 启动 Hub 事件循环（广播告警），随 ctx 取消或 Hub 关闭而返回。
func (h *Hub) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.done:
			return
		case e := <-h.alertCh:
			b, err := json.Marshal(map[string]interface{}{"type": "alert", "data": e})
			if err != nil {
				continue
			}
			h.broadcast(b)
		}
	}
}

// broadcast 向所有已注册客户端非阻塞投递。整个遍历在 h.mu 内完成，
// 与 CloseClients / Unregister 的「删除 + close(send)」互斥。
func (h *Hub) broadcast(b []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case c.send <- b:
		default:
		}
	}
}

// enqueue 在 h.mu 内确认客户端仍注册后，向 send 非阻塞投递一条消息。
// 关闭 send（Unregister / CloseClients）也在同一临界区内完成，
// 因此这里绝不会向已关闭的 channel 发送。
func (h *Hub) enqueue(c *Client, b []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[c]; !ok {
		return
	}
	select {
	case c.send <- b:
	default:
	}
}

// Register 注册客户端，返回是否注册成功。
// Hub 已关闭时拒绝注册并主动关闭传入连接，避免停机过程中出现
// 「已升级但无人管理」的 WebSocket 连接。
func (h *Hub) Register(c *Client) bool {
	h.mu.Lock()
	select {
	case <-h.done:
		h.mu.Unlock()
		if c.conn != nil {
			_ = c.conn.Close()
		}
		return false
	default:
	}
	h.clients[c] = true
	h.mu.Unlock()
	return true
}

// Unregister 注销客户端；幂等：已注销（或已被 CloseClients 关闭）时不重复关闭 channel。
func (h *Hub) Unregister(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[c]; !ok {
		return
	}
	delete(h.clients, c)
	if c.send != nil {
		close(c.send)
	}
	if c.done != nil {
		close(c.done)
	}
}

// CloseClients 幂等关闭 Hub：停止指标推送、注销并断开所有已升级的 WS 连接。
// 由服务器优雅退出流程在**停机开始时**调用（信号上下文取消之后、Shutdown 排空
// 在途请求之前），使 WS 推送与每秒一次的 TSDB 查询在排空窗口内即停止，不必让
// Shutdown 去等待长连接；不关闭 alertCh（告警引擎仍可能短暂广播）。
func (h *Hub) CloseClients() {
	h.stopOnce.Do(func() {
		close(h.done)
		h.mu.Lock()
		conns := make([]*websocket.Conn, 0, len(h.clients))
		for c := range h.clients {
			delete(h.clients, c)
			if c.send != nil {
				close(c.send)
			}
			if c.done != nil {
				close(c.done)
			}
			if c.conn != nil {
				conns = append(conns, c.conn)
			}
		}
		h.mu.Unlock()
		// 锁外关闭底层连接，唤醒阻塞在 ReadMessage 的 readPump（其 defer Unregister
		// 会因 map 中已无该客户端而直接返回，不会二次关闭 channel）。
		for _, conn := range conns {
			_ = conn.Close()
		}
	})
}

// ClientCount 返回当前已注册的 WebSocket 客户端数量（自监控用）。
func (h *Hub) ClientCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

// BroadcastAlert 广播告警事件给所有 WS 客户端。
// Hub 已关闭（停机窗口）时静默丢弃：Run 可能已退出，继续入队只会刷
// 「告警广播队列已满」的日志噪声，且不会有任何客户端收到。
func (h *Hub) BroadcastAlert(e model.AlertEvent) {
	select {
	case <-h.done:
		return
	default:
	}
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
			// node 是**查询参数**目标：必须 fail-closed（checkNodeTarget）。若用 checkNodeScope，
			// 受限用户传未注册 node 会握手成功（101），随后 pushNodeMetrics 会把 TSDB 中该标签的
			// 实时指标推送出去——正是要堵的「未注册节点数据 / 判别 oracle」。此处握手尚未 Upgrade，
			// 直接写 403 是安全可行的。
			if !a.checkNodeTarget(w, r, perm, node) {
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
	client := newWSClient(h, conn)
	if !h.Register(client) {
		// Hub 已关闭（服务器正在退出）：Register 已关闭该连接，不再启动协程。
		return
	}

	topic := r.URL.Query().Get("topic")
	node := r.URL.Query().Get("node")

	go func() { _ = client.writePump() }()
	go client.readPump()

	if topic == "metrics" && node != "" {
		go h.pushNodeMetrics(client, store, node)
	}
}

// pushNodeMetrics 每 1s 查询 VM 最新指标并推送给客户端。
// Hub 关闭或该客户端注销/断线后立即退出，不再继续查询 TSDB。
func (h *Hub) pushNodeMetrics(client *Client, store storage.Storage, node string) {
	metrics := []string{"cpu_usage", "mem_used_percent", "disk_used_percent",
		"swap_used_percent", "network_recv_rate", "network_sent_rate",
		"network_recv_total", "network_sent_total",
		"load1", "load5", "load15", "disk_read_rate", "disk_write_rate"}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	// 连续空周期计数：payload 长期为 0 意味着 TSDB 查询失败或该节点指标未上报，
	// 前端会一直停留在「正在等待实时数据」。此处打节流日志（首次 + 每 30 次）
	// 帮助从 server 日志直接定位根因。
	var emptyCycles int
	var lastErr error
	for {
		select {
		case <-h.done:
			return
		case <-client.done:
			// 客户端已注销（readPump 断线或 CloseClients）：停止查询与推送。
			return
		case <-ticker.C:
			var payload []map[string]interface{}
			recordErr := func(err error) {
				if err != nil {
					lastErr = err
				}
			}
			for _, name := range metrics {
				switch {
				case name == "disk_used_percent":
					p, err := aggregateDiskUsageForNode(store, node)
					recordErr(err)
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
					recordErr(err)
					if err != nil || p == nil {
						continue
					}
					payload = append(payload, map[string]interface{}{
						"name": name, "value": p.Value, "timestamp": p.Timestamp,
					})
				default:
					p, err := store.QueryLatest(node, name, nil)
					recordErr(err)
					if err != nil || p == nil {
						continue
					}
					payload = append(payload, map[string]interface{}{
						"name": name, "value": p.Value, "timestamp": p.Timestamp,
					})
				}
			}
			if len(payload) > 0 {
				emptyCycles = 0
				b, _ := json.Marshal(map[string]interface{}{"type": "metrics", "node": node, "data": payload})
				// 经 enqueue 写入：写 send 前在 h.mu 内确认客户端仍在 clients 中，
				// 避免与 CloseClients / Unregister 的 close(send) 竞态 panic。
				h.enqueue(client, b)
			} else {
				emptyCycles++
				if emptyCycles == 1 || emptyCycles%30 == 0 {
					slog.Warn("WS 实时推送无可用数据（前端将停留在等待提示）",
						"node", node, "连续空周期", emptyCycles, "lastErr", lastErr)
				}
			}
		}
	}
}

// writePump 发送循环，返回导致其退出的写错误（send 被关闭时返回 nil）。
// 写失败或 send 被关闭时一并关闭底层连接，促使阻塞在 ReadMessage 的 readPump
// 退出并注销，避免断线客户端残留。
//
// 同时按 defaultWSPingInterval 发送 ping 心跳：即使没有业务数据推送（如节点查询
// 不到实时指标），也保持链路有流量，避免被反代/LB 的空闲超时掐断后陷入「前端
// 3s 重连循环」。gorilla 禁止并发写，因此 ping 必须与数据写在同一循环内完成。
//
// 每个客户端恰好启动一个 writePump（唯一写者）；退出时关闭 writeDone，
// 便于测试在该写者消失后再探测连接状态。
func (c *Client) writePump() error {
	// 注册顺序保证 LIFO 执行时 conn.Close() 先于 close(writeDone)：
	// 观察到 writeDone 关闭即意味着底层连接已关闭。
	defer close(c.writeDone)
	if c.conn == nil {
		// 防御：无底层连接的客户端（测试构造）不得 panic。
		return nil
	}
	defer c.conn.Close()
	pingTicker := time.NewTicker(defaultWSPingInterval)
	defer pingTicker.Stop()
	for {
		select {
		case msg, ok := <-c.send:
			if !ok {
				return nil
			}
			// 每次写前刷新写截止时间：客户端停止读取（零窗口）时阻塞写会在
			// writeWait 后变成错误（os.ErrDeadlineExceeded），从而走
			// return → conn.Close() → readPump 退出 → Unregister 的收敛链，
			// 不会无限挂住。
			_ = c.conn.SetWriteDeadline(time.Now().Add(c.writeWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return err
			}
		case <-pingTicker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(c.writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return err
			}
		}
	}
}

// readPump 读循环，检测断开后注销（关闭 send 与 done 以通知 pushNodeMetrics 退出）。
//
// 配合 writePump 的 ping 心跳设置读截止时间：超过 defaultWSPongWait 未收到任何
// pong/数据帧判定链路死亡，主动退出并注销，避免半开连接的推送协程按秒空转查询 TSDB。
func (c *Client) readPump() {
	defer c.hub.Unregister(c)
	if c.conn == nil {
		// 防御：无底层连接的客户端（测试构造）不得 panic；与 writePump 守卫对称。
		return
	}
	// 收到 pong（浏览器对服务端 ping 的自动回应）或任意数据帧都刷新读截止时间。
	_ = c.conn.SetReadDeadline(time.Now().Add(defaultWSPongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(defaultWSPongWait))
	})
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(defaultWSPongWait))
	}
}
