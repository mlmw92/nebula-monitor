package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/server/config"
)

// MaxEvents 内存中保留的最大审计事件条数，超出按时间淘汰。
const MaxEvents = 2000

// Event 记录一次管理接口操作，不保存请求体或敏感参数。
type Event struct {
	Time           time.Time `json:"time"`
	User           string    `json:"user"`
	Method         string    `json:"method"`
	Path           string    `json:"path"`
	Status         int       `json:"status"`
	RemoteIP       string    `json:"remoteIP"`
	SourceLocation string    `json:"sourceLocation,omitempty"` // 来源 IP 属地（国家/省份/城市），由 Server 端经 ip2region 补全
	Succeeded      bool      `json:"succeeded"`
	Category       string    `json:"category,omitempty"`
	Action         string    `json:"action,omitempty"`
	Detail         string    `json:"detail,omitempty"`
}

// Store 以 JSON 文件持久化审计事件，提供记录与查询能力。
type Store struct {
	mu     sync.RWMutex
	path   string
	events []Event
}

// New 创建审计存储并加载已有事件；path 为空时仅内存模式。
func New(path string) *Store {
	s := &Store{path: path, events: make([]Event, 0)}
	if path == "" {
		return s
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	var events []Event
	if json.Unmarshal(data, &events) == nil {
		s.events = trim(events)
	}
	return s
}

// Record 追加一条审计事件并持久化（超过上限自动截断到最近 MaxEvents 条）。
func (s *Store) Record(event Event) error {
	if event.Time.IsZero() {
		event.Time = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
	s.events = trim(s.events)
	if s.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(s.events, "", "  ")
	if err != nil {
		return err
	}
	return config.AtomicWrite(s.path, data)
}

// List 返回最近的审计事件（按时间倒序），可按用户与路径子串过滤。
func (s *Store) List(limit int, user, path string) []Event {
	return s.ListFiltered(limit, user, path, "")
}

// Count 返回当前保留的审计事件条数。
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.events)
}

// ListFiltered 在 List 基础上额外按事件分类过滤。
func (s *Store) ListFiltered(limit int, user, path, category string) []Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > MaxEvents {
		limit = 100
	}
	result := make([]Event, 0, limit)
	for i := len(s.events) - 1; i >= 0 && len(result) < limit; i-- {
		e := s.events[i]
		if user != "" && e.User != user {
			continue
		}
		if path != "" && !strings.Contains(e.Path, path) {
			continue
		}
		if category != "" && e.Category != category {
			continue
		}
		result = append(result, e)
	}
	return result
}

// SummarizeChange 返回持久化对象的语义化前后差异摘要。
func SummarizeChange(before, after interface{}) string {
	beforeMap := normalizeObject(before)
	afterMap := normalizeObject(after)
	changed := make([]string, 0)
	added := make([]string, 0)
	removed := make([]string, 0)
	for key, beforeValue := range beforeMap {
		afterValue, ok := afterMap[key]
		if !ok {
			removed = append(removed, key)
			continue
		}
		if canonicalJSON(beforeValue) != canonicalJSON(afterValue) {
			changed = append(changed, key)
		}
	}
	for key := range afterMap {
		if _, ok := beforeMap[key]; !ok {
			added = append(added, key)
		}
	}
	sort.Strings(changed)
	sort.Strings(added)
	sort.Strings(removed)
	return "before_sha256=" + objectHash(beforeMap) +
		" after_sha256=" + objectHash(afterMap) +
		" changed=" + strings.Join(changed, ",") +
		" added=" + strings.Join(added, ",") +
		" removed=" + strings.Join(removed, ",")
}

func normalizeObject(value interface{}) map[string]interface{} {
	data, err := json.Marshal(value)
	if err != nil {
		return map[string]interface{}{}
	}
	var object map[string]interface{}
	if json.Unmarshal(data, &object) != nil {
		return map[string]interface{}{}
	}
	return sanitizeObject(object)
}

func sanitizeObject(object map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{}, len(object))
	for key, value := range object {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "password") || strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "credential") {
			result[key] = "<redacted>"
			continue
		}
		if nested, ok := value.(map[string]interface{}); ok {
			result[key] = sanitizeObject(nested)
			continue
		}
		result[key] = value
	}
	return result
}

func canonicalJSON(value interface{}) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func objectHash(object map[string]interface{}) string {
	sum := sha256.Sum256([]byte(canonicalJSON(object)))
	return hex.EncodeToString(sum[:])[:16]
}

func trim(events []Event) []Event {
	if len(events) <= MaxEvents {
		return events
	}
	return events[len(events)-MaxEvents:]
}

// ClientIP 从请求中提取客户端 IP（去除端口部分）。
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

// RedactPath 返回请求路径，用于审计记录。
func RedactPath(r *http.Request) string { return r.URL.Path }
