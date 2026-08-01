package alert

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// AckInfo 记录一条告警事件的确认（认领）信息。
type AckInfo struct {
	Rule     string `json:"rule"`
	Host     string `json:"host"`
	Instance string `json:"instance"`
	User     string `json:"user,omitempty"`
	Time     int64  `json:"time"` // 确认时间（毫秒）
}

// AckStore 以 JSON 文件持久化告警确认状态，按 rule|host|instance 去重。
// 与 monitor_alert 时序库解耦，避免污染 firing/resolved 状态序列。
type AckStore struct {
	mu   sync.RWMutex
	acks map[string]AckInfo
	path string
}

// NewAckStore 创建确认存储并加载。
func NewAckStore(path string) *AckStore {
	s := &AckStore{acks: map[string]AckInfo{}, path: path}
	s.load()
	return s
}

func ackKey(rule, host, instance string) string { return rule + "|" + host + "|" + instance }

func (s *AckStore) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var list []AckInfo
	if err := json.Unmarshal(data, &list); err != nil {
		return
	}
	for _, a := range list {
		s.acks[ackKey(a.Rule, a.Host, a.Instance)] = a
	}
}

// Mark 确认（认领）一条告警。
func (s *AckStore) Mark(rule, host, instance, user string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acks[ackKey(rule, host, instance)] = AckInfo{
		Rule:     rule,
		Host:     host,
		Instance: instance,
		User:     user,
		Time:     time.Now().UnixMilli(),
	}
	s.persistLocked()
}

// Map 返回全部确认状态快照，key 为 rule|host|instance。
func (s *AckStore) Map() map[string]AckInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]AckInfo, len(s.acks))
	for k, v := range s.acks {
		out[k] = v
	}
	return out
}

func (s *AckStore) persistLocked() {
	list := make([]AckInfo, 0, len(s.acks))
	for _, v := range s.acks {
		list = append(list, v)
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(dirOf(s.path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(s.path, data, 0o644)
}
