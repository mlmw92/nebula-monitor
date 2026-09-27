package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/storage"
)

// hubWSStore 只替代外部 TSDB 读取（真实 Hub / 真实 WS 连接保持不变），
// 并对查询计数，便于断言 Hub 关闭后 pushNodeMetrics 不再查询。
type hubWSStore struct {
	storage.Storage
	instantCalls atomic.Int64
}

func (s *hubWSStore) QueryInstant(node, name string, labels map[string]string) ([]model.Series, error) {
	s.instantCalls.Add(1)
	return []model.Series{{
		Labels: map[string]string{"node": node, "device": "sda"},
		Points: []model.Point{{Timestamp: 1000, Value: 1}},
	}}, nil
}

func (s *hubWSStore) QueryLatest(node, name string, labels map[string]string) (*model.Point, error) {
	s.instantCalls.Add(1)
	return &model.Point{Timestamp: 1000, Value: 1}, nil
}

// newWSHub 启动带真实 WebSocket 端点的 Hub。绕过 a.wsAuthorize（授权已在
// scope_hostname_test.go 覆盖），直接挂 handleWS，专注生命周期行为。
func newWSHub(t *testing.T, store storage.Storage) (*Hub, *httptest.Server) {
	t.Helper()
	h := NewHub()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		h.handleWS(store, w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return h, srv
}

func dialWS(t *testing.T, srv *httptest.Server, query string) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws" + query
	conn, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("WebSocket 拨号失败: %v (resp=%v)", err, resp)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func waitClientCount(t *testing.T, h *Hub, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if h.ClientCount() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待 WS 客户端数 %d 超时，当前 %d", want, h.ClientCount())
}

// hubClientsSnapshot 取当前已注册客户端，供测试直接调用 Unregister（幂等路径）。
func hubClientsSnapshot(h *Hub) []*Client {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		out = append(out, c)
	}
	return out
}

// waitClosed 断言连接在超时内被服务端关闭（ReadMessage 返回错误）。
// 仅用于「服务端从未向该连接写过数据」的场景（如 alerts 客户端、被拒连接）：
// 此时 FIN 立即可见。若连接上可能有积压帧，请改用 drainConn + 服务端状态断言——
// Windows 在发送缓冲仍有积压数据时 close，可能长时间不向对端送达 FIN/RST。
func waitClosed(t *testing.T, conn *websocket.Conn, why string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("%s：客户端连接未被关闭", why)
		}
		_ = conn.SetReadDeadline(deadline)
		if _, _, err := conn.ReadMessage(); err != nil {
			var nerr net.Error
			if errors.As(err, &nerr) && nerr.Timeout() {
				t.Fatalf("%s：等待连接关闭超时", why)
			}
			return
		}
	}
}

// drainConn 尽力读走残余帧，直到连接报错或空闲为止，不做断言。
// 平台说明同 waitClosed：对端读不到关闭/阻塞在残余数据上属平台相关的 FIN 延迟，
// 故客户端可观测的收敛由服务端状态（ClientCount、底层 conn 是否可写）与
// TestWS_DisconnectUnregistersClientAndStopsQueries 断言。
func drainConn(conn *websocket.Conn) {
	_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

// waitWritePumpDone 等待该客户端的 writePump 真正退出（writeDone 关闭）。
// gorilla 的 *websocket.Conn 只允许一个并发写者（生产里唯一写者就是 writePump，
// 见 beginMessage/flushFrame 均只读写侧状态）；测试若要探测服务端连接状态，必须先
// 确认写者已消失，否则会与其收尾写入竞态（CI 的 -race 曾报 beginMessage 与
// flushFrame 数据竞争）。等到 writeDone 关闭，同时也意味着 writePump 的
// defer c.conn.Close() 已执行完毕。
func waitWritePumpDone(t *testing.T, c *Client) {
	t.Helper()
	select {
	case <-c.writeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("writePump 未退出，无法在唯一写者消失后再探测连接状态")
	}
}

// probeServerConnClosed 在「唯一写者已退出」的前提下探测服务端底层连接是否已关闭，
// 返回该次写的错误（nil 表示仍可写，即未关闭）。
// 直接写原始 net.Conn：既绕开 gorilla 的 writeErr 缓存（结论只取决于连接本身是否
// 被关闭），也不触碰 gorilla Conn 的读侧状态，故与可能仍在收尾的 readPump 无交互。
func probeServerConnClosed(c *Client) error {
	_, err := c.conn.UnderlyingConn().Write([]byte("after-close"))
	return err
}

// TestHubCloseClients 覆盖：真实连接、幂等重复关闭、ClientCount 归零、
// 关闭后 Unregister 不 panic、关闭前后 BroadcastAlert 均不 panic。
func TestHubCloseClients(t *testing.T) {
	h, srv := newWSHub(t, nil)
	conn := dialWS(t, srv, "?topic=alerts")
	waitClientCount(t, h, 1)
	clients := hubClientsSnapshot(h)

	// 关闭前广播：不得 panic。
	h.BroadcastAlert(model.AlertEvent{Node: "web-01"})

	h.CloseClients()
	h.CloseClients() // 幂等：重复调用不得 panic

	if n := h.ClientCount(); n != 0 {
		t.Fatalf("残留 WS 客户端: %d", n)
	}
	// 服务端状态断言（确定性强）：CloseClients 必须真正关闭底层连接，而非仅从 map 移除。
	// 唯一写者：先等 writePump 退出（writeDone），此后再无任何 goroutine 会写这个连接。
	waitWritePumpDone(t, clients[0])
	if err := probeServerConnClosed(clients[0]); err == nil {
		t.Fatal("CloseClients 后服务端底层连接仍可写，连接未被关闭")
	}
	waitClosed(t, conn, "CloseClients 后")

	// 关闭后再注销不得 panic（Unregister 的 map 存在性守卫）。
	clients[0].hub.Unregister(clients[0])
	if n := h.ClientCount(); n != 0 {
		t.Fatalf("Unregister 后客户端数 = %d, want 0", n)
	}

	// 关闭后广播不得 panic（不得 close(alertCh)：告警引擎仍可能短暂调用），
	// 且应静默丢弃（见 TestHubCloseClients_BroadcastAfterCloseSilent）。
	h.BroadcastAlert(model.AlertEvent{Node: "web-01"})
}

// TestHubCloseClients_BroadcastAfterCloseSilent 关闭后广播应静默丢弃，
// 不再刷「告警广播队列已满」WARN。控制组先验证未关闭时确实会打该日志，
// 避免日志捕获本身失效导致假绿。
func TestHubCloseClients_BroadcastAfterCloseSilent(t *testing.T) {
	var buf bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	const warnMsg = "告警广播队列已满"
	h := NewHub()

	// 控制组：未关闭且无人消费 alertCh，写满后必须出现 WARN。
	for i := 0; i < cap(h.alertCh)+1; i++ {
		h.BroadcastAlert(model.AlertEvent{Node: "web-01"})
	}
	if !strings.Contains(buf.String(), warnMsg) {
		t.Fatalf("控制组失效：未关闭时写满未记录 WARN，日志=%q", buf.String())
	}
	buf.Reset()

	// Hub 关闭后：Run 可能已退出，广播必须静默丢弃，不得刷日志。
	h.CloseClients()
	for i := 0; i < cap(h.alertCh)+1; i++ {
		h.BroadcastAlert(model.AlertEvent{Node: "web-01"})
	}
	if got := buf.String(); strings.Contains(got, warnMsg) {
		t.Fatalf("关闭后广播仍产生 WARN 日志: %q", got)
	}
}

// TestHubRun_ExitsOnContextCancel 覆盖 Run(ctx) 随上下文取消退出。
func TestHubRun_ExitsOnContextCancel(t *testing.T) {
	h := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Run(ctx)
	}()

	h.BroadcastAlert(model.AlertEvent{Node: "web-01"}) // 取消前广播：缓冲不阻塞
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run 未随 ctx 取消退出")
	}

	// Run 退出后广播仍不得 panic。
	h.BroadcastAlert(model.AlertEvent{Node: "web-01"})
}

// TestHubRun_BroadcastsToClient 确认 Run 的广播路径仍能投递（守卫不得吞掉正常发送）。
func TestHubRun_BroadcastsToClient(t *testing.T) {
	h, srv := newWSHub(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.Run(ctx)

	conn := dialWS(t, srv, "?topic=alerts")
	waitClientCount(t, h, 1)
	h.BroadcastAlert(model.AlertEvent{Node: "web-01"})

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("未收到告警广播: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(msg, &m); err != nil {
		t.Fatalf("广播消息不是合法 JSON: %v", err)
	}
	if m["type"] != "alert" {
		t.Fatalf("消息类型 = %v, want alert", m["type"])
	}
}

// TestHubCloseClients_EnqueueSkipsClosedSend 确定性覆盖核心风险点：
// 已注销（send 已关闭）的客户端再经 enqueue 写入时必须静默跳过，
// 否则就是 «send on closed channel» panic 的确定性复现。
func TestHubCloseClients_EnqueueSkipsClosedSend(t *testing.T) {
	h := NewHub()
	c := newWSClient(h, nil)
	h.Register(c)

	h.enqueue(c, []byte("before"))
	select {
	case got := <-c.send:
		if string(got) != "before" {
			t.Fatalf("投递内容 = %q, want before", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("在册客户端未收到 enqueue 投递")
	}

	h.Unregister(c) // 关闭 c.send
	h.enqueue(c, []byte("after-unregister"))

	h.CloseClients() // 再次关闭：幂等
	h.enqueue(c, []byte("after-close"))

	// 未注册客户端不得被再次写入：send 已关闭，写入会 panic（此处到不了即可）。
	if n := h.ClientCount(); n != 0 {
		t.Fatalf("客户端数 = %d, want 0", n)
	}
}

// TestHubCloseClients_RunBroadcastSkipsClosedSend 覆盖广播路径：
// 已注销并关闭 send 的客户端不得再被 Run 广播写入（否则 panic）。
func TestHubCloseClients_RunBroadcastSkipsClosedSend(t *testing.T) {
	h := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.Run(ctx)

	c := newWSClient(h, nil)
	h.Register(c)
	h.BroadcastAlert(model.AlertEvent{Node: "web-01"})

	select {
	case <-c.send:
	case <-time.After(3 * time.Second):
		t.Fatal("广播未投递到已注册客户端")
	}

	h.Unregister(c) // 关闭 send：此后广播路径绝不能写入该 channel
	for i := 0; i < 64; i++ {
		h.BroadcastAlert(model.AlertEvent{Node: "web-01"})
	}
	time.Sleep(100 * time.Millisecond) // 让 Run 处理队列；若写入已关闭 channel 会 panic
}

// TestWS_MetricsClientKeepsStreaming 覆盖 metrics 客户端持续连接：能连续收到多帧指标，
// 不因发送路径竞争（pushNodeMetrics 与 writePump 抢 client.send）被丢弃或提前断开。
func TestWS_MetricsClientKeepsStreaming(t *testing.T) {
	store := &hubWSStore{}
	h, srv := newWSHub(t, store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.Run(ctx)

	conn := dialWS(t, srv, "?topic=metrics&node=web-01")
	waitClientCount(t, h, 1)

	got := 0
	deadline := time.Now().Add(6 * time.Second)
	for got < 2 && time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(4 * time.Second))
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("metrics 客户端被提前断开（已收 %d 帧）: %v", got, err)
		}
		var m map[string]any
		if err := json.Unmarshal(msg, &m); err != nil {
			t.Fatalf("指标消息不是合法 JSON: %v", err)
		}
		if m["type"] != "metrics" {
			t.Fatalf("消息类型 = %v, want metrics", m["type"])
		}
		got++
	}
	if got < 2 {
		t.Fatalf("未持续收到指标帧: %d", got)
	}
}

// TestWS_PushNodeMetricsStopsAfterClose 覆盖 ticker 在连接关闭后不再查询 TSDB。
func TestWS_PushNodeMetricsStopsAfterClose(t *testing.T) {
	store := &hubWSStore{}
	h, srv := newWSHub(t, store)
	conn := dialWS(t, srv, "?topic=metrics&node=web-01")
	waitClientCount(t, h, 1)
	clients := hubClientsSnapshot(h)

	deadline := time.Now().Add(4 * time.Second)
	for store.instantCalls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if store.instantCalls.Load() == 0 {
		t.Fatal("pushNodeMetrics 未查询 TSDB")
	}

	h.CloseClients()
	// 服务端状态断言（确定性强）：连接必须被真正关闭。唯一写者：先等 writePump 退出
	// 后再探测，避免与它的收尾写竞态；客户端侧能否及时读到关闭受平台 FIN 延迟影响
	// （见 drainConn 注释），故不作硬断言。
	waitWritePumpDone(t, clients[0])
	if err := probeServerConnClosed(clients[0]); err == nil {
		t.Fatal("CloseClients 后服务端底层连接仍可写，连接未被关闭")
	}
	// 自适应基准：先等注销生效（ClientCount 归零），再等一个 tick 周期让在途查询
	// 收尾，然后取基准并在下一个 tick 周期后断言不再增长，减少调度抖动误报。
	closeDeadline := time.Now().Add(3 * time.Second)
	for h.ClientCount() != 0 && time.Now().Before(closeDeadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := h.ClientCount(); n != 0 {
		t.Fatalf("CloseClients 后仍残留客户端: %d", n)
	}
	time.Sleep(1200 * time.Millisecond)
	base := store.instantCalls.Load()
	time.Sleep(1500 * time.Millisecond) // 超过一个 tick 周期
	if n := store.instantCalls.Load(); n > base {
		t.Fatalf("关闭后仍继续查询 TSDB: base=%d now=%d", base, n)
	}
	drainConn(conn) // 客户端侧仅尽力排空，不作断言（平台 FIN 延迟）
}

// TestWS_PushNodeMetricsStopsOnUnregister 单独注销（客户端断线）时推送协程也必须退出，
// 不能继续按秒查询 TSDB。此处用无底层连接的客户端直接驱动推送循环。
func TestWS_PushNodeMetricsStopsOnUnregister(t *testing.T) {
	store := &hubWSStore{}
	h := NewHub()
	c := newWSClient(h, nil)
	h.Register(c)

	pushDone := make(chan struct{})
	go func() {
		defer close(pushDone)
		h.pushNodeMetrics(c, store, "web-01")
	}()

	deadline := time.Now().Add(4 * time.Second)
	for store.instantCalls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if store.instantCalls.Load() == 0 {
		t.Fatal("pushNodeMetrics 未查询 TSDB")
	}

	h.Unregister(c) // 关 send：推送协程必须退出，且不得向已关闭 channel 写入
	select {
	case <-pushDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Unregister 后 pushNodeMetrics 未退出")
	}

	base := store.instantCalls.Load()
	time.Sleep(1500 * time.Millisecond)
	if n := store.instantCalls.Load(); n > base {
		t.Fatalf("注销后仍继续查询 TSDB: base=%d now=%d", base, n)
	}
}

// pipeHijackWriter 是 Upgrade 所需的 ResponseWriter 替身：Hijack 交出一根 net.Pipe 端
// 作为底层连接，从而让服务端 *websocket.Conn 跑在完全可控、无缓冲的内存传输上。
// 101 响应由 Upgrade 直接写 netConn（不经过本 Writer），故 Write/Header 只满足签名。
type pipeHijackWriter struct {
	conn net.Conn
	hdr  http.Header
}

func (w *pipeHijackWriter) Header() http.Header {
	if w.hdr == nil {
		w.hdr = http.Header{}
	}
	return w.hdr
}
func (w *pipeHijackWriter) WriteHeader(int)             {}
func (w *pipeHijackWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *pipeHijackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.conn, bufio.NewReadWriter(
		bufio.NewReaderSize(w.conn, 4096),
		bufio.NewWriterSize(io.Discard, 4096),
	), nil
}

// upgradeOverPipe 借助 Hijacker 替身在 net.Pipe 上完成一次真实 WS 握手，返回服务端
// *websocket.Conn。Upgrade 会把 101 响应直接写到 netConn，因此 peer 只读走该响应，
// 之后不再读取任何数据（此后服务端写必然阻塞）。
func upgradeOverPipe(t *testing.T, conn, peer net.Conn) *websocket.Conn {
	t.Helper()
	handshakeRead := make(chan struct{})
	go func() {
		defer close(handshakeRead)
		// 只读到头部结束（\r\n\r\n），不读 body：避免任何形式的“替 Upgrade 排空”。
		_ = peer.SetReadDeadline(time.Now().Add(3 * time.Second))
		br := bufio.NewReaderSize(peer, 4096)
		line, err := br.ReadString('\n')
		if err != nil || !strings.HasPrefix(line, "HTTP/1.1 101") {
			t.Errorf("net.Pipe 握手响应异常: line=%q err=%v", line, err)
			return
		}
		for {
			line, err = br.ReadString('\n')
			if err != nil {
				t.Errorf("net.Pipe 握手响应读取失败: %v", err)
				return
			}
			if line == "\r\n" || line == "\n" {
				return
			}
		}
	}()

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")

	wsConn, err := upgrader.Upgrade(&pipeHijackWriter{conn: conn}, req, nil)
	if err != nil {
		t.Fatalf("net.Pipe 上的 WS 握手失败: %v", err)
	}
	t.Cleanup(func() { _ = wsConn.Close() })
	<-handshakeRead // 确保 101 已读走，此后 peer 不再读取
	return wsConn
}

// TestWS_WritePumpBlocksOnlyUpToWriteDeadline 覆盖「客户端只连不读」场景：
// 阻塞的写必须在写截止时间到期时以超时错误结束，并关闭底层连接。
//
// 构造（确定性，不依赖平台缓冲/自动调优语义）：传输用 net.Pipe——无内核缓冲，
// 对端不读时 Write 必然阻塞，且支持 SetWriteDeadline。经 Hijacker 替身完成真实
// WS 握手后只跑 writePump，不启动 readPump（读侧错误会掩盖写侧行为）。写截止
// 时间按连接注入（c.writeWait），不改任何全局。
//
// 反证唯一性：主断言前 peer 已停止读取且不会被再次读取，因此「写未阻塞」在
// 前提断言处就会报错；若写前不设截止时间，写会永久阻塞，唯一可能的失败是
// 「写阻塞未被写截止时间打断（deadline 未生效）」或前提断言，绝无第二种解释。
func TestWS_WritePumpBlocksOnlyUpToWriteDeadline(t *testing.T) {
	serverEnd, peerEnd := net.Pipe()
	t.Cleanup(func() { _ = peerEnd.Close() })

	h := NewHub()
	c := newWSClient(h, upgradeOverPipe(t, serverEnd, peerEnd))
	c.writeWait = 400 * time.Millisecond
	h.Register(c)

	pumpErr := make(chan error, 1)
	go func() { pumpErr <- c.writePump() }()
	h.enqueue(c, []byte("blocked"))

	// 前提断言（确定性）：对端不读 ⇒ 写确实阻塞，writePump 不会自行返回。
	select {
	case err := <-pumpErr:
		t.Fatalf("写未阻塞（writePump 提前返回 err=%v），前提不成立", err)
	case <-time.After(100 * time.Millisecond):
	}

	// 主断言：写截止时间到期必须打断阻塞，并返回超时错误。
	// 注意：gorilla 的 hideTempErr 会把超时错误包装成自带的 netError（丢掉
	// os.ErrDeadlineExceeded 哨兵但保留 Timeout()），故断言 Timeout() 而非哨兵。
	select {
	case err := <-pumpErr:
		var nerr net.Error
		if err == nil || !errors.As(err, &nerr) || !nerr.Timeout() {
			t.Fatalf("writePump 错误 = %v, want 超时错误（写未被写截止时间打断）", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("写阻塞未被写截止时间打断（deadline 未生效）")
	}

	// 有牙断言：writePump 的结束路径必须关闭底层连接。net.Pipe 对端的 Read 只在
	// 「本端被 Close」时立刻返回 io.EOF；本用例从未关闭 serverEnd/peerEnd，
	// 因此该结果只能由 writePump 的 defer c.conn.Close() 产生（否则读到超时）。
	_ = peerEnd.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := peerEnd.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("writePump 结束路径未关闭底层连接：对端 Read err=%v, want io.EOF", err)
	}
}

// TestWS_DisconnectUnregistersClientAndStopsQueries 端到端断线回归护栏：
// 客户端异常断开（无 WS 关闭帧，直接断底层 TCP）后，readPump 的 defer Unregister
// 必须把客户端移出 Hub，且 pushNodeMetrics 必须在约一个 tick 内停止查询 TSDB。
func TestWS_DisconnectUnregistersClientAndStopsQueries(t *testing.T) {
	store := &hubWSStore{}
	h, srv := newWSHub(t, store)
	conn := dialWS(t, srv, "?topic=metrics&node=web-01")
	waitClientCount(t, h, 1)

	deadline := time.Now().Add(4 * time.Second)
	for store.instantCalls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if store.instantCalls.Load() == 0 {
		t.Fatal("pushNodeMetrics 未查询 TSDB")
	}

	// 异常断开：无关闭帧，直接断底层 TCP（模拟客户端停止读取后进程消失）。
	_ = conn.UnderlyingConn().Close()

	deadline = time.Now().Add(5 * time.Second)
	for h.ClientCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := h.ClientCount(); n != 0 {
		t.Fatalf("客户端异常断开后仍残留注册: %d", n)
	}

	// 自适应基准：注销后再等一个 tick 周期，让在途查询收尾，再断言不再增长。
	time.Sleep(1200 * time.Millisecond)
	base := store.instantCalls.Load()
	time.Sleep(1500 * time.Millisecond)
	if n := store.instantCalls.Load(); n > base {
		t.Fatalf("断开后仍继续查询 TSDB: base=%d now=%d", base, n)
	}
}

// TestWS_RegisterRejectedAfterClose Hub 已关闭时新连接必须被拒绝并主动关闭，
// 避免停机过程中出现「已升级但无人管理」的连接。
func TestWS_RegisterRejectedAfterClose(t *testing.T) {
	h, srv := newWSHub(t, nil)
	h.CloseClients()

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws?topic=alerts"
	conn, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		// 拒绝路径会立即关闭连接，客户端可能读不到 101。但「报错」不能是模糊状态：
		// 若响应已声明握手成功（101）却仍返回错误，说明是别的故障，必须失败。
		if resp != nil && resp.StatusCode == http.StatusSwitchingProtocols {
			t.Fatalf("Dial 报错但响应已是 101 Switching Protocols，拒绝路径异常: %v", err)
		}
		t.Logf("新连接已被拒绝（握手未成功）: err=%v resp=%v", err, resp)
	} else {
		defer conn.Close()
		waitClosed(t, conn, "Hub 关闭后新连接")
	}
	if n := h.ClientCount(); n != 0 {
		t.Fatalf("Hub 关闭后仍注册了新客户端: %d", n)
	}
}

// TestHubCloseClients_ConcurrentSendAndUnregister 并发发送与关闭的竞态回归：
// 广播（Run）、指标推送（pushNodeMetrics）、CloseClients、Unregister 同时进行，
// 在 -race 下不得出现数据竞争或向已关闭 channel 发送的 panic。
func TestHubCloseClients_ConcurrentSendAndUnregister(t *testing.T) {
	store := &hubWSStore{}
	h, srv := newWSHub(t, store)
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		h.Run(ctx)
	}()

	for i := 0; i < 4; i++ {
		dialWS(t, srv, "?topic=alerts")
	}
	for i := 0; i < 2; i++ {
		dialWS(t, srv, "?topic=metrics&node=web-01")
	}
	waitClientCount(t, h, 6)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					h.BroadcastAlert(model.AlertEvent{Node: "web-01"})
				}
			}
		}()
	}

	clients := hubClientsSnapshot(h)
	wg.Add(2)
	go func() {
		defer wg.Done()
		h.CloseClients()
	}()
	go func() {
		defer wg.Done()
		// 与 CloseClients 并发逐个注销（幂等守卫 + 推送协程退出路径）。
		for _, c := range clients {
			c.hub.Unregister(c)
		}
	}()

	// 覆盖至少一个 ticker 周期，让 pushNodeMetrics 的发送与关闭真正交错。
	time.Sleep(1500 * time.Millisecond)
	close(stop)
	h.CloseClients() // 重复关闭
	wg.Wait()
	cancel()
	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Run 未退出")
	}

	if n := h.ClientCount(); n != 0 {
		t.Fatalf("残留客户端: %d", n)
	}
	h.BroadcastAlert(model.AlertEvent{Node: "web-01"}) // 关闭后不得 panic
}
