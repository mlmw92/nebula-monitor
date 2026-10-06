package receiver

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/logstore"
)

// 集中日志上行（C2）。
//
// 与指标上报（POST /api/v1/report）**刻意分成两个接口**：
// 日志体积大、可以丢、按周期批量；指标小而必须可靠。混在一个请求里，
// 一次日志洪峰就会拖垮指标上报的延迟与成功率——而指标断了才是真正的事故。

// logDefaultMaxBody 是单次日志上行请求体的兜底上限（4 MiB）。
const logDefaultMaxBody = 4 << 20

// logDefaultRateBps 是单节点上行速率的兜底上限（1 MiB/s）。
const logDefaultRateBps = 1 << 20

// SetLogStore 注入集中日志存储器（未注入 = 该能力关闭，接口返回 503）。
//
// 参数是接口：传 nil **具体类型**（例如未配置目录的 *Store）会得到"非 nil 接口"，
// 上面的 503 判断随即失效——注入前必须显式判空（见 cmd/server 的写法）。
func (r *Receiver) SetLogStore(store logstore.LogStore, maxBodyBytes, rateBps int64) {
	if maxBodyBytes <= 0 {
		maxBodyBytes = logDefaultMaxBody
	}
	if rateBps <= 0 {
		rateBps = logDefaultRateBps
	}
	r.logs = store
	r.logMaxBody = maxBodyBytes
	r.logLimiter = &logRateLimiter{bps: rateBps, buckets: map[string]*logBucket{}}
}

// HandleLogs 处理 POST /api/v1/logs。
func (r *Receiver) HandleLogs(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// 复用同一套接入授权：日志上行与指标上报来自同一批 Agent，不应有第二套凭据
	if !r.agentAuthorized(req) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.logs == nil {
		// 明确区分「没启用」与「写失败」：运维看到 503 就知道该去配 logDir
		http.Error(w, "central logs disabled", http.StatusServiceUnavailable)
		return
	}

	body := http.MaxBytesReader(w, req.Body, r.logMaxBody)
	var batch model.LogBatch
	if err := json.NewDecoder(body).Decode(&batch); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, fmt.Sprintf("request too large (limit %d bytes)", r.logMaxBody), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !model.IsValidLogSourceName(batch.Source) {
		http.Error(w, "invalid source name", http.StatusBadRequest)
		return
	}
	if len(batch.Lines) == 0 {
		writeLogResult(w, model.LogAppendResult{})
		return
	}
	if !r.logSourceDeclared(batch.Node, batch.Source) {
		http.Error(w, "source not declared by node", http.StatusForbidden)
		return
	}

	// 限速：按请求体量计（日志的代价就是字节数）。超限回 429 —— 这是**正常结果**，
	// Agent 侧会记成 log_dropped_total{reason=rate} 而不是「上传失败」。
	if r.logLimiter != nil && !r.logLimiter.allow(batch.Node, estimateBytes(batch)) {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "rate limited", http.StatusTooManyRequests)
		return
	}

	accepted, dropped, reason, err := r.logs.Append(batch)
	if err != nil {
		// 外部后端不可用要回 502（Agent 会重试并记指标），而不是 400——
		// 400 会让现场去查 Agent 的配置，而真实原因是"日志后端连不上"。
		if errors.Is(err, logstore.ErrBackendUnavailable) {
			http.Error(w, "store unavailable: "+err.Error(), http.StatusBadGateway)
			return
		}
		http.Error(w, "store failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeLogResult(w, model.LogAppendResult{Accepted: accepted, Dropped: dropped, Reason: reason})
}

// agentAuthorized 复用 Agent 接入授权校验（常量时间比较，防时序侧信道）。
func (r *Receiver) agentAuthorized(req *http.Request) bool {
	if !r.auth.Enabled {
		return true
	}
	got := req.Header.Get("X-Agent-Secret")
	return subtle.ConstantTimeCompare([]byte(got), []byte(r.auth.Secret)) == 1
}

// logSourceDeclared 判断该节点的确声明了这个日志来源。
//
// 节点未知、或未声明任何来源时一律放行：Agent 可能**先发日志、后上报能力**，
// 卡在这里会把启动后的首轮日志整批打回。来源名本身受限（model.IsValidLogSourceName）
// 且存储布局固定，因此这不构成「任意写入通道」；这个检查的价值在于
// 「节点已经声明了来源清单」之后，阻止往清单之外的目录写。
func (r *Receiver) logSourceDeclared(node, source string) bool {
	if r.nodeMgr == nil || node == "" {
		return true
	}
	n, ok := r.nodeMgr.GetNode(node)
	if !ok || len(n.LogSources) == 0 {
		return true
	}
	for _, s := range n.LogSources {
		if s == source {
			return true
		}
	}
	return false
}

// logBodyLimit 返回日志请求体上限（未注入存储时回兜底值）。
func (r *Receiver) logBodyLimit() int64 {
	if r.logMaxBody <= 0 {
		return logDefaultMaxBody
	}
	return r.logMaxBody
}

// estimateBytes 估算本批的字节量（用文本长度，足够做速率控制）。
func estimateBytes(b model.LogBatch) int {
	n := 0
	for _, l := range b.Lines {
		n += len(l.Text) + 64 // 近似的每行开销（JSON 字段与标签）
	}
	return n
}

func writeLogResult(w http.ResponseWriter, res model.LogAppendResult) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(res)
}

// logBucket 是单节点的令牌桶。
type logBucket struct {
	tokens float64
	last   time.Time
}

// logRateLimiter 按节点做简单令牌桶限速（内存态）。
//
// 桶容量取 1 秒的量：允许一次性突发一秒的数据量（采集是周期性的、天然成批），
// 但持续的超出必然被拦。内存占用与节点数成正比，且有界。
type logRateLimiter struct {
	mu      sync.Mutex
	bps     int64
	buckets map[string]*logBucket
}

// allow 判断能否放行 n 字节；不足则不放行（同时把已累积的令牌记下来，不回退时间）。
func (l *logRateLimiter) allow(node string, n int) bool {
	if l.bps <= 0 {
		return true
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[node]
	if !ok {
		b = &logBucket{tokens: float64(l.bps), last: now}
		l.buckets[node] = b
	}
	b.tokens = min(float64(l.bps), b.tokens+now.Sub(b.last).Seconds()*float64(l.bps))
	b.last = now
	if b.tokens < float64(n) {
		return false
	}
	b.tokens -= float64(n)
	return true
}
