package alert

import (
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/server/config"
)

// 告警处置状态（D4 协作状态机）。
const (
	// StatusPending 待处理：尚未认领，或已被「重新打开」。
	StatusPending = "pending"
	// StatusAck 已认领：有人接手处理。
	StatusAck = "ack"
	// StatusClosed 已关闭：处置完成（含误报关闭），需给出原因。
	StatusClosed = "closed"
)

// 协作记录的容量上限，避免 JSON 文件无界膨胀。
const (
	maxAckComments   = 50   // 单条告警保留的最新评论条数
	maxCommentLength = 2000 // 单条评论的字符上限
)

// ErrEmptyComment 表示评论内容为空。
var ErrEmptyComment = errors.New("评论内容不能为空")

// Comment 是告警处置过程中的一条协作评论。
type Comment struct {
	User string `json:"user"`
	Text string `json:"text"`
	Time int64  `json:"time"`
}

// AckInfo 记录一条告警的处置状态（认领 / 指派 / 关闭）与协作评论。
//
// 兼容性：`status` 为 D4 新增字段。旧记录没有该字段，`EffectiveStatus` 一律按
// 「已认领」处理，从而保证升级后既有的确认记录不会被当成待处理而重新冒出来。
type AckInfo struct {
	Rule     string `json:"rule"`
	Host     string `json:"host"`
	Instance string `json:"instance"`
	StartsAt int64  `json:"startsAt"` // 被处置告警的触发时间（毫秒）

	Status      string    `json:"status,omitempty"`
	User        string    `json:"user,omitempty"`     // 最近一次操作人
	Assignee    string    `json:"assignee,omitempty"` // 当前处理人（认领时=本人，指派时=被指派人）
	Time        int64     `json:"time"`               // 最近一次操作时间（毫秒）
	AckTime     int64     `json:"ackTime,omitempty"`
	CloseTime   int64     `json:"closeTime,omitempty"`
	CloseReason string    `json:"closeReason,omitempty"`
	Comments    []Comment `json:"comments,omitempty"`
}

// EffectiveStatus 返回生效状态：空值（D4 之前的记录）视为「已认领」。
func (i AckInfo) EffectiveStatus() string {
	if i.Status == "" {
		return StatusAck
	}
	return i.Status
}

// Handled 表示该告警是否已有人处理（已认领或已关闭）。
// 「待处理」（含重新打开）与无记录都返回 false。
func (i AckInfo) Handled() bool {
	switch i.EffectiveStatus() {
	case StatusAck, StatusClosed:
		return true
	}
	return false
}

// AckStore 以 JSON 文件持久化告警处置状态，按 rule|host|instance|startsAt 去重。
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

func ackKey(rule, host, instance string, startsAt int64) string {
	return rule + "|" + host + "|" + instance + "|" + strconv.FormatInt(startsAt, 10)
}

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
		s.acks[ackKey(a.Rule, a.Host, a.Instance, a.StartsAt)] = a
	}
}

// update 以「读-改-写」方式应用一次处置变更；记录不存在时初始化为待处理。
// actor 会被记录为「最近一次操作人」，供协作时间线与审计追溯。
func (s *AckStore) update(rule, host, instance string, startsAt int64, actor string, fn func(*AckInfo)) AckInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := ackKey(rule, host, instance, startsAt)
	info, exists := s.acks[key]
	if !exists {
		info = AckInfo{
			Rule:     rule,
			Host:     host,
			Instance: instance,
			StartsAt: startsAt,
			Status:   StatusPending,
		}
	}
	fn(&info)
	info.User = actor
	info.Time = time.Now().UnixMilli()
	s.acks[key] = info
	s.persistLocked()
	return info
}

// Mark 认领一条告警（处理人记为操作者本人）。保留既有签名以兼容调用方。
func (s *AckStore) Mark(rule, host, instance string, startsAt int64, user string) AckInfo {
	return s.update(rule, host, instance, startsAt, user, func(i *AckInfo) {
		i.Status = StatusAck
		i.Assignee = user
		if i.AckTime == 0 {
			i.AckTime = time.Now().UnixMilli()
		}
	})
}

// Assign 把告警指派给指定处理人（同时置为已认领）。
// assignee 为空时表示指派给当前操作者本人。
func (s *AckStore) Assign(rule, host, instance string, startsAt int64, user, assignee string) AckInfo {
	if strings.TrimSpace(assignee) == "" {
		assignee = user
	}
	return s.update(rule, host, instance, startsAt, user, func(i *AckInfo) {
		i.Status = StatusAck
		i.Assignee = strings.TrimSpace(assignee)
		if i.AckTime == 0 {
			i.AckTime = time.Now().UnixMilli()
		}
	})
}

// Close 关闭告警并记录原因。
func (s *AckStore) Close(rule, host, instance string, startsAt int64, user, reason string) AckInfo {
	return s.update(rule, host, instance, startsAt, user, func(i *AckInfo) {
		i.Status = StatusClosed
		i.CloseReason = strings.TrimSpace(reason)
		i.CloseTime = time.Now().UnixMilli()
	})
}

// Reopen 重新打开告警：回到待处理并清空处理人与关闭信息。
// 评论历史与认领时间保留，便于回看处置过程。
func (s *AckStore) Reopen(rule, host, instance string, startsAt int64, user string) AckInfo {
	return s.update(rule, host, instance, startsAt, user, func(i *AckInfo) {
		i.Status = StatusPending
		i.Assignee = ""
		i.CloseReason = ""
		i.CloseTime = 0
	})
}

// Comment 追加一条协作评论。评论不改变处置状态；记录不存在时建立待处理记录。
func (s *AckStore) Comment(rule, host, instance string, startsAt int64, user, text string) (AckInfo, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return AckInfo{}, ErrEmptyComment
	}
	if runes := []rune(text); len(runes) > maxCommentLength {
		text = string(runes[:maxCommentLength])
	}
	now := time.Now().UnixMilli()
	info := s.update(rule, host, instance, startsAt, user, func(i *AckInfo) {
		i.Comments = append(i.Comments, Comment{User: user, Text: text, Time: now})
		// 只保留最新的若干条，避免单条告警的协作记录无界增长
		if len(i.Comments) > maxAckComments {
			i.Comments = i.Comments[len(i.Comments)-maxAckComments:]
		}
	})
	return info, nil
}

// Get 返回单条告警的处置记录。
func (s *AckStore) Get(rule, host, instance string, startsAt int64) (AckInfo, bool) {
	if s == nil {
		return AckInfo{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	info, ok := s.acks[ackKey(rule, host, instance, startsAt)]
	return info, ok
}

// IsHandled 返回该告警是否已有人处理（认领或关闭）。
// 注意语义变化：被「重新打开」的告警（pending）会重新回到待处理列表。
func (s *AckStore) IsHandled(rule, host, instance string, startsAt int64) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	info, ok := s.acks[ackKey(rule, host, instance, startsAt)]
	return ok && info.Handled()
}

// Map 返回全部处置记录快照，key 为 rule|host|instance|startsAt。
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
	if err := config.AtomicWrite(s.path, data); err != nil {
		return
	}
}
