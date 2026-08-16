package defense

import (
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// banEventCollector 增量采集 fail2ban action 写入的封禁审计 JSONL。
// 仅读取新增字节（offset 增量），支持日志轮转安全重置，单周期事件限流以防洪峰。
type banEventCollector struct {
	mu     sync.Mutex
	offset int64
	path   string
	offsetPath string
}

const (
	// maxBanEventsPerCycle 单个采集周期最多处理的封禁事件条数。
	maxBanEventsPerCycle = 200
)

// NewBanEventCollector 创建封禁事件采集器并加载既有 offset。
func NewBanEventCollector() *banEventCollector {
	c := &banEventCollector{
		path:       auditPath,
		offsetPath: stateDir + "/ban_offset.json",
	}
	c.loadOffset()
	return c
}

func (c *banEventCollector) loadOffset() {
	data, err := os.ReadFile(c.offsetPath)
	if err != nil {
		return
	}
	var o struct {
		Offset int64 `json:"offset"`
	}
	if json.Unmarshal(data, &o) == nil {
		c.offset = o.Offset
	}
}

func (c *banEventCollector) saveOffset() {
	data, _ := json.Marshal(struct {
		Offset int64 `json:"offset"`
	}{Offset: c.offset})
	_ = os.WriteFile(c.offsetPath, data, 0600)
}

// auditRecord 对应 action 追加的 JSON 行。
type auditRecord struct {
	Action   string `json:"action"`
	IP       string `json:"ip"`
	Jail     string `json:"jail"`
	Failures string `json:"failures"`
	Time     string `json:"time"`
}

// Collect 读取自上次 offset 以来的新增封禁/解封记录，转换为安全事件。
// node 为当前 agent 节点名。
func (c *banEventCollector) Collect(node string) []model.SecurityEvent {
	c.mu.Lock()
	defer c.mu.Unlock()

	var events []model.SecurityEvent
	f, err := os.Open(c.path)
	if err != nil {
		return events
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return events
	}
	size := st.Size()
	if size < c.offset {
		// 文件被轮转/截断，从头读取
		c.offset = 0
	}
	if _, err := f.Seek(c.offset, 0); err != nil {
		return events
	}

	buf := make([]byte, 64*1024)
	var line []byte
	now := time.Now().UnixMilli()
	count := 0
	for count < maxBanEventsPerCycle {
		n, e := f.Read(buf)
		if n == 0 {
			break
		}
		chunk := buf[:n]
		for i := 0; i < n; i++ {
			if chunk[i] == '\n' {
				line = append(line, chunk[:i]...)
				if len(line) > 0 {
					var rec auditRecord
					if json.Unmarshal(line, &rec) == nil && rec.IP != "" {
						events = append(events, toBanEvent(node, rec, now))
						count++
						if count >= maxBanEventsPerCycle {
							break
						}
					}
				}
				line = line[:0]
			} else {
				line = append(line, chunk[i])
			}
		}
		if e != nil {
			break
		}
	}
	c.offset = size
	c.saveOffset()
	if count >= maxBanEventsPerCycle {
		slog.Warn("封禁事件单周期已达上限，余下延后采集", "path", c.path, "limit", maxBanEventsPerCycle)
	}
	return events
}

func toBanEvent(node string, rec auditRecord, now int64) model.SecurityEvent {
	sev := model.SeverityInfo
	msg := "SSH 解封 " + rec.IP
	if rec.Action == "ban" {
		sev = model.SeverityWarning
		msg = "SSH 暴力破解封禁 " + rec.IP
	}
	return model.SecurityEvent{
		Category:  model.SecurityCatBan,
		Severity:  sev,
		Message:   msg,
		Node:      node,
		SourceIP:  rec.IP,
		Timestamp: now,
		Detail:    map[string]string{"jail": rec.Jail, "failures": rec.Failures},
		BanInfo: &model.BanInfo{
			Action:   rec.Action,
			IP:       rec.IP,
			Jail:     rec.Jail,
			Failures: rec.Failures,
		},
	}
}
