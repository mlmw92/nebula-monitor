package alert

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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

// AckStore 持久化告警处置状态，按 rule|host|instance|startsAt 去重。
// 与 monitor_alert 时序库解耦，避免污染 firing/resolved 状态序列。
//
// 两种模式（设计件批次 18）：
//   - **入库模式**（db != nil）：写台账库的 alert_acks 表，一次变更写一行；
//     内存 map 保留为**读缓存**——告警引擎的 IsHandled 在热路径上，不该每次都查库。
//   - **降级模式**（db == nil）：退回 JSON 文件，每次变更全量重写（原行为）。
type AckStore struct {
	mu   sync.RWMutex
	acks map[string]AckInfo
	path string
	db   *sql.DB
}

// NewAckStore 创建确认存储并加载。
func NewAckStore(path string) *AckStore {
	s := &AckStore{acks: map[string]AckInfo{}, path: path}
	s.load()
	return s
}

const upsertAckSQL = `INSERT INTO alert_acks(
	ack_key, rule, host, instance, starts_at, status, user_name, assignee,
	time_ms, ack_time_ms, close_time_ms, close_reason, comments
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(ack_key) DO UPDATE SET
	status=excluded.status, user_name=excluded.user_name, assignee=excluded.assignee,
	time_ms=excluded.time_ms, ack_time_ms=excluded.ack_time_ms,
	close_time_ms=excluded.close_time_ms, close_reason=excluded.close_reason,
	comments=excluded.comments`

func ackArgs(key string, a AckInfo) ([]any, error) {
	comments := a.Comments
	if comments == nil {
		comments = []Comment{}
	}
	data, err := json.Marshal(comments)
	if err != nil {
		return nil, err
	}
	// 状态写**生效值**：迁移前的老记录没有 status 字段（空串），而空串在语义上等于
	// 「已认领」。统一成显式值，避免库里的空串与内存里的判定各说各话。
	return []any{
		key, a.Rule, a.Host, a.Instance, a.StartsAt, a.EffectiveStatus(), a.User, a.Assignee,
		a.Time, a.AckTime, a.CloseTime, a.CloseReason, string(data),
	}, nil
}

// readJSONAcks 读 JSON 处置文件；文件不存在返回 (nil, nil)，**内容坏掉才返回错误**
// ——调用方据此决定"不回填也不改名"，把现场保住。
func readJSONAcks(path string) ([]AckInfo, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取告警处置文件失败: %w", err)
	}
	var list []AckInfo
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("解析告警处置文件失败（已保留原文件，未回填）: %w", err)
	}
	return list, nil
}

// insertAcks 单事务批量写入（回填用）。
func insertAcks(db *sql.DB, list []AckInfo) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("回填告警处置失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(upsertAckSQL)
	if err != nil {
		return fmt.Errorf("回填告警处置失败: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	for _, a := range list {
		args, err := ackArgs(ackKey(a.Rule, a.Host, a.Instance, a.StartsAt), a)
		if err != nil {
			return fmt.Errorf("回填告警处置失败: %w", err)
		}
		if _, err := stmt.Exec(args...); err != nil {
			return fmt.Errorf("回填告警处置失败: %w", err)
		}
	}
	return tx.Commit()
}

func loadAcksFromDB(db *sql.DB) (map[string]AckInfo, error) {
	rows, err := db.Query(`SELECT ack_key, rule, host, instance, starts_at, status, user_name,
		assignee, time_ms, ack_time_ms, close_time_ms, close_reason, comments FROM alert_acks`)
	if err != nil {
		return nil, fmt.Errorf("读取告警处置失败: %w", err)
	}
	defer rows.Close()
	out := map[string]AckInfo{}
	for rows.Next() {
		var (
			key, status, comments string
			a                     AckInfo
		)
		if err := rows.Scan(&key, &a.Rule, &a.Host, &a.Instance, &a.StartsAt, &status, &a.User,
			&a.Assignee, &a.Time, &a.AckTime, &a.CloseTime, &a.CloseReason, &comments); err != nil {
			return nil, fmt.Errorf("读取告警处置失败: %w", err)
		}
		a.Status = status
		if comments != "" && comments != "[]" {
			var list []Comment
			if json.Unmarshal([]byte(comments), &list) == nil {
				a.Comments = list
			}
		}
		out[key] = a
	}
	return out, rows.Err()
}

// UseSQLite 把存储切到入库模式：库里为空且存在 JSON 文件时**一次性回填**（幂等判据是
// 「表为空」而不是「文件存在」，重复启动不会重复导入），随后把原文件改名 .bak-migrated；
// 最后以库为准重建内存读缓存（库里有数据就以库为准）。
//
// 回填失败不阻断启动：返回错误由调用方记日志，本存储继续按降级模式工作。
func (s *AckStore) UseSQLite(db *sql.DB) error {
	if db == nil {
		return nil
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM alert_acks").Scan(&count); err != nil {
		return fmt.Errorf("读取告警处置表失败: %w", err)
	}
	if count == 0 {
		list, err := readJSONAcks(s.path)
		if err != nil {
			return err
		}
		if len(list) > 0 {
			if err := insertAcks(db, list); err != nil {
				return err
			}
		}
		if s.path != "" {
			if _, statErr := os.Stat(s.path); statErr == nil {
				if err := os.Rename(s.path, s.path+".bak-migrated"); err != nil {
					return fmt.Errorf("改名已迁移的处置文件失败: %w", err)
				}
			}
		}
	}
	loaded, err := loadAcksFromDB(db)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.db = db
	s.acks = loaded
	s.mu.Unlock()
	return nil
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
func (s *AckStore) update(rule, host, instance string, startsAt int64, actor string, fn func(*AckInfo)) (AckInfo, error) {
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
	if err := s.saveLocked(key, info); err != nil {
		return AckInfo{}, err
	}
	s.acks[key] = info
	return info, nil
}

// saveLocked 落盘一条记录：入库模式 upsert 一行；降级模式全量重写 JSON。
func (s *AckStore) saveLocked(key string, info AckInfo) error {
	if s.db == nil {
		return s.persistWithLocked(key, info)
	}
	args, err := ackArgs(key, info)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(upsertAckSQL, args...); err != nil {
		return fmt.Errorf("保存告警处置失败: %w", err)
	}
	return nil
}

// Apply 在一次持久化写入中完成状态变更与可选评论。
func (s *AckStore) Apply(rule, host, instance string, startsAt int64, actor string,
	fn func(*AckInfo), comment string) (AckInfo, error) {
	comment = strings.TrimSpace(comment)
	return s.update(rule, host, instance, startsAt, actor, func(i *AckInfo) {
		fn(i)
		if comment == "" {
			return
		}
		if runes := []rune(comment); len(runes) > maxCommentLength {
			comment = string(runes[:maxCommentLength])
		}
		i.Comments = append(i.Comments, Comment{User: actor, Text: comment, Time: time.Now().UnixMilli()})
		if len(i.Comments) > maxAckComments {
			i.Comments = i.Comments[len(i.Comments)-maxAckComments:]
		}
	})
}

// Mark 认领一条告警（处理人记为操作者本人）。
func (s *AckStore) Mark(rule, host, instance string, startsAt int64, user string) (AckInfo, error) {
	return s.update(rule, host, instance, startsAt, user, func(i *AckInfo) {
		i.Status = StatusAck
		i.Assignee = user
		if i.AckTime == 0 {
			i.AckTime = time.Now().UnixMilli()
		}
		// 认领把记录从「已关闭」拉回处理中：清掉上一条的关闭信息，避免状态残留。
		i.CloseReason = ""
		i.CloseTime = 0
	})
}

// Assign 把告警指派给指定处理人（同时置为已认领）。
// assignee 为空时表示指派给当前操作者本人。
func (s *AckStore) Assign(rule, host, instance string, startsAt int64, user, assignee string) (AckInfo, error) {
	if strings.TrimSpace(assignee) == "" {
		assignee = user
	}
	return s.update(rule, host, instance, startsAt, user, func(i *AckInfo) {
		i.Status = StatusAck
		i.Assignee = strings.TrimSpace(assignee)
		if i.AckTime == 0 {
			i.AckTime = time.Now().UnixMilli()
		}
		i.CloseReason = ""
		i.CloseTime = 0
	})
}

// Close 关闭告警并记录原因。
func (s *AckStore) Close(rule, host, instance string, startsAt int64, user, reason string) (AckInfo, error) {
	return s.update(rule, host, instance, startsAt, user, func(i *AckInfo) {
		i.Status = StatusClosed
		i.CloseReason = strings.TrimSpace(reason)
		i.CloseTime = time.Now().UnixMilli()
	})
}

// Reopen 重新打开告警：回到待处理并清空处理人与关闭信息。
// 评论历史与认领时间保留，便于回看处置过程。
func (s *AckStore) Reopen(rule, host, instance string, startsAt int64, user string) (AckInfo, error) {
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
	info, err := s.update(rule, host, instance, startsAt, user, func(i *AckInfo) {
		i.Comments = append(i.Comments, Comment{User: user, Text: text, Time: now})
		// 只保留最新的若干条，避免单条告警的协作记录无界增长
		if len(i.Comments) > maxAckComments {
			i.Comments = i.Comments[len(i.Comments)-maxAckComments:]
		}
	})
	return info, err
}

// AckStats 是处置记录的统计快照（保留策略与自监控使用）。
type AckStats struct {
	Total   int   `json:"total"`
	Handled int   `json:"handled"`
	Oldest  int64 `json:"oldest"` // 最早一条记录的最后操作时间（毫秒）；无记录为 0
}

// Stats 返回处置记录统计。
func (s *AckStore) Stats() AckStats {
	if s == nil {
		return AckStats{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := AckStats{Total: len(s.acks)}
	for _, v := range s.acks {
		if v.Handled() {
			out.Handled++
		}
		if v.Time > 0 && (out.Oldest == 0 || v.Time < out.Oldest) {
			out.Oldest = v.Time
		}
	}
	return out
}

// PruneHandled 删除「已处置（已认领或已关闭）」且最后操作时间早于 before 的记录，返回删除条数。
//
// 待处理（pending）记录一律保留：它们仍然需要人处理，不能因为时间久远被静默清掉，
// 否则「重新打开后回到待处理」的告警会在清理后再次消失。
func (s *AckStore) PruneHandled(before int64) (int, error) {
	if s == nil || before <= 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0)
	for key, info := range s.acks {
		if info.Handled() && info.Time < before {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return 0, nil
	}
	if s.db != nil {
		if _, err := s.db.Exec(`DELETE FROM alert_acks WHERE status IN ('ack','closed','') AND time_ms < ?`, before); err != nil {
			return 0, fmt.Errorf("清理告警处置失败: %w", err)
		}
	} else if s.path != "" {
		remaining := make([]AckInfo, 0, len(s.acks)-len(keys))
		remove := make(map[string]struct{}, len(keys))
		for _, key := range keys {
			remove[key] = struct{}{}
		}
		for key, info := range s.acks {
			if _, ok := remove[key]; !ok {
				remaining = append(remaining, info)
			}
		}
		if err := writeAcks(s.path, remaining); err != nil {
			return 0, err
		}
	}
	for _, key := range keys {
		delete(s.acks, key)
	}
	return len(keys), nil
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

func (s *AckStore) persistWithLocked(key string, info AckInfo) error {
	if s.path == "" {
		return nil
	}
	list := make([]AckInfo, 0, len(s.acks)+1)
	for k, v := range s.acks {
		if k != key {
			list = append(list, v)
		}
	}
	list = append(list, info)
	return writeAcks(s.path, list)
}

func (s *AckStore) persistLocked() {
	if s.path == "" {
		return
	}
	list := make([]AckInfo, 0, len(s.acks))
	for _, v := range s.acks {
		list = append(list, v)
	}
	_ = writeAcks(s.path, list)
}

func writeAcks(path string, list []AckInfo) error {
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return fmt.Errorf("编码告警处置失败: %w", err)
	}
	if err := os.MkdirAll(dirOf(path), 0o755); err != nil {
		return fmt.Errorf("创建告警处置目录失败: %w", err)
	}
	if err := config.AtomicWrite(path, data); err != nil {
		return fmt.Errorf("保存告警处置失败: %w", err)
	}
	return nil
}
