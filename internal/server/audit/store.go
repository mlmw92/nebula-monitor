package audit

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/server/config"
)

// MaxEvents 是**降级模式**下保留的最大审计事件条数（入库模式不再用它做保留口径）。
//
// 入库之后保留改成**时间**（retention 的 AuditDays）加一个兜底条数上限：审计的价值就在
// "能查到多久以前"，只保留最近 N 条等于搬了个家。这个常量只对"台账库打不开、退回 JSON"
// 的降级路径仍然有意义。
const MaxEvents = 2000

// MaxRows 是审计表的**兜底条数上限**（时间保留之外的磁盘安全网）。
//
// 时间口径（retention 的 AuditDays）才是主规则；留这条兜底的理由很实际：
// 一台被脚本刷接口的机器能在 180 天里写出几百万条审计，把单机磁盘写爆。
const MaxRows = 500000

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

// Store 持久化审计事件，提供记录与查询能力。
//
// 两种模式：
//   - **入库模式**（db != nil，生产路径）：事件写进台账库的 audit_events 表，查询走 SQL
//     （可按人 / 时间 / 路径 / 分类过滤并分页）。设计件：批次 18。
//   - **降级模式**（db == nil）：退回 JSON 文件 + 内存切片，只留最近 MaxEvents 条。
//     台账库打不开时审计不该跟着失效——它与台账是两件事。
type Store struct {
	mu     sync.RWMutex
	path   string
	db     *sql.DB
	events []Event // 仅降级模式使用
}

// New 创建审计存储并加载已有事件；path 为空时仅内存模式（测试用）。
func New(path string) *Store {
	s := &Store{path: path, events: make([]Event, 0)}
	events, err := readJSONEvents(path)
	if err != nil {
		return s
	}
	s.events = trim(events)
	return s
}

// migratedSuffix 是回填完成后原 JSON 文件的新后缀（不删：既是证据，也是回滚旧版本时
// 让旧版本重新看到迁移前快照的办法）。
const migratedSuffix = ".bak-migrated"

// readJSONEvents 读 JSON 审计文件；文件不存在返回 (nil, nil)，**内容坏掉才返回错误**
// ——调用方据此决定"不回填也不改名"，把现场保住。
func readJSONEvents(path string) ([]Event, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取审计文件失败: %w", err)
	}
	var events []Event
	if err := json.Unmarshal(data, &events); err != nil {
		return nil, fmt.Errorf("解析审计文件失败（已保留原文件，未回填）: %w", err)
	}
	return events, nil
}

const insertEventSQL = `INSERT INTO audit_events(
	time_ms, user_name, method, path, status, remote_ip, source_loc, succeeded, category, action, detail
) VALUES(?,?,?,?,?,?,?,?,?,?,?)`

func eventArgs(e Event) []any {
	succeeded := 0
	if e.Succeeded {
		succeeded = 1
	}
	return []any{
		e.Time.UnixMilli(), e.User, e.Method, e.Path, e.Status, e.RemoteIP,
		e.SourceLocation, succeeded, e.Category, e.Action, e.Detail,
	}
}

// insertEvents 单事务批量写入（回填用）。
func insertEvents(db *sql.DB, events []Event) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("回填审计事件失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(insertEventSQL)
	if err != nil {
		return fmt.Errorf("回填审计事件失败: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	for _, e := range events {
		if _, err := stmt.Exec(eventArgs(e)...); err != nil {
			return fmt.Errorf("回填审计事件失败: %w", err)
		}
	}
	return tx.Commit()
}

// UseSQLite 把存储切到入库模式：库里有数据就以库为准；库里为空且存在 JSON 文件时
// **一次性回填**，随后把原文件改名 .bak-migrated。
//
// 幂等判据是「表为空」而不是「文件存在」：回填只可能发生一次，重复启动不会重复导入，
// 也不需要给事件硬造一个唯一键（造出来的键只会引入假数据）。
//
// 回填失败**不阻断启动**：返回错误由调用方记日志，本存储继续按降级模式工作。
// 宁可丢历史，也不能因为一份坏 JSON 让服务起不来（设计件 §4）。
func (s *Store) UseSQLite(db *sql.DB) error {
	if db == nil {
		return nil
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM audit_events").Scan(&count); err != nil {
		return fmt.Errorf("读取审计表失败: %w", err)
	}
	if count == 0 && s.path != "" {
		events, err := readJSONEvents(s.path)
		if err != nil {
			return err
		}
		if len(events) > 0 {
			if err := insertEvents(db, events); err != nil {
				return err
			}
		}
		if _, statErr := os.Stat(s.path); statErr == nil {
			if err := os.Rename(s.path, s.path+migratedSuffix); err != nil {
				return fmt.Errorf("改名已迁移的审计文件失败: %w", err)
			}
		}
	}
	s.mu.Lock()
	s.db = db
	s.events = nil // 入库模式下不再维护内存副本，读走 SQL
	s.mu.Unlock()
	return nil
}

// Record 追加一条审计事件。入库模式写一行；降级模式写 JSON（超过上限截断到最近 MaxEvents 条）。
func (s *Store) Record(event Event) error {
	if event.Time.IsZero() {
		event.Time = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		_, err := s.db.Exec(insertEventSQL, eventArgs(event)...)
		return err
	}
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

// QueryFilter 是审计查询条件。From/To 为毫秒时间戳（0 = 不限）。
type QueryFilter struct {
	User     string
	Path     string
	Category string
	From     int64
	To       int64
	Limit    int
	Offset   int
}

// auditWhere 生成**列表与计数共用**的 WHERE 片段。
//
// 共用一处是刻意的：两处各写一遍，迟早出现"计数说 12、列表只有 3 条"这类自相矛盾的界面。
func auditWhere(f QueryFilter) (string, []any) {
	where := " WHERE 1=1"
	args := make([]any, 0, 6)
	if f.User != "" {
		where += " AND user_name=?"
		args = append(args, f.User)
	}
	if f.Path != "" {
		// 子串匹配沿用既有语义（原实现是 strings.Contains）。用 instr 而不是 LIKE：
		// LIKE 会把路径里的 % 与 _ 当通配符，还得额外转义。
		where += " AND instr(path, ?) > 0"
		args = append(args, f.Path)
	}
	if f.Category != "" {
		where += " AND category=?"
		args = append(args, f.Category)
	}
	if f.From > 0 {
		where += " AND time_ms >= ?"
		args = append(args, f.From)
	}
	if f.To > 0 {
		where += " AND time_ms <= ?"
		args = append(args, f.To)
	}
	return where, args
}

// Query 返回符合条件的审计事件（时间倒序）与**同条件的总数**。
// 入库模式走 SQL 分页；降级模式在内存切片上做同样的过滤（台账库不可用时仍能查）。
func (s *Store) Query(f QueryFilter) ([]Event, int, error) {
	if f.Limit <= 0 || f.Limit > MaxEvents {
		f.Limit = 100
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	s.mu.RLock()
	db := s.db
	s.mu.RUnlock()
	if db == nil {
		return s.queryMemory(f)
	}
	where, args := auditWhere(f)
	var total int
	if err := db.QueryRow("SELECT COUNT(*) FROM audit_events"+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("统计审计事件失败: %w", err)
	}
	rows, err := db.Query(`SELECT time_ms, user_name, method, path, status, remote_ip,
		source_loc, succeeded, category, action, detail FROM audit_events`+where+
		" ORDER BY time_ms DESC, id DESC LIMIT ? OFFSET ?", append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("查询审计事件失败: %w", err)
	}
	defer rows.Close()
	out := make([]Event, 0, f.Limit)
	for rows.Next() {
		var (
			e         Event
			timeMs    int64
			succeeded int
		)
		if err := rows.Scan(&timeMs, &e.User, &e.Method, &e.Path, &e.Status, &e.RemoteIP,
			&e.SourceLocation, &succeeded, &e.Category, &e.Action, &e.Detail); err != nil {
			return nil, 0, fmt.Errorf("查询审计事件失败: %w", err)
		}
		e.Time = time.UnixMilli(timeMs)
		e.Succeeded = succeeded != 0
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// queryMemory 是降级模式的查询实现：过滤语义与原 ListFiltered 一致，另加时间范围与 offset。
func (s *Store) queryMemory(f QueryFilter) ([]Event, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	matched := make([]Event, 0, len(s.events))
	for i := len(s.events) - 1; i >= 0; i-- {
		e := s.events[i]
		if f.User != "" && e.User != f.User {
			continue
		}
		if f.Path != "" && !strings.Contains(e.Path, f.Path) {
			continue
		}
		if f.Category != "" && e.Category != f.Category {
			continue
		}
		if f.From > 0 && e.Time.UnixMilli() < f.From {
			continue
		}
		if f.To > 0 && e.Time.UnixMilli() > f.To {
			continue
		}
		matched = append(matched, e)
	}
	total := len(matched)
	if f.Offset >= total {
		return []Event{}, total, nil
	}
	matched = matched[f.Offset:]
	if len(matched) > f.Limit {
		matched = matched[:f.Limit]
	}
	return matched, total, nil
}

// List 返回最近的审计事件（按时间倒序），可按用户与路径子串过滤。
func (s *Store) List(limit int, user, path string) []Event {
	return s.ListFiltered(limit, user, path, "")
}

// ListFiltered 在 List 基础上额外按事件分类过滤。
func (s *Store) ListFiltered(limit int, user, path, category string) []Event {
	events, _, _ := s.Query(QueryFilter{User: user, Path: path, Category: category, Limit: limit})
	return events
}

// Count 返回当前保留的审计事件条数（入库模式为表行数）。
func (s *Store) Count() int {
	s.mu.RLock()
	db := s.db
	s.mu.RUnlock()
	if db == nil {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return len(s.events)
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM audit_events").Scan(&n); err != nil {
		return 0
	}
	return n
}

// Prune 删除早于 before（毫秒）的事件，返回删除条数（入库模式的保留策略）。
//
// 降级模式不做任何事：它的保留口径本来就是"最近 MaxEvents 条"，没有时间语义。
func (s *Store) Prune(before int64) (int, error) {
	if before <= 0 {
		return 0, nil
	}
	s.mu.RLock()
	db := s.db
	s.mu.RUnlock()
	if db == nil {
		return 0, nil
	}
	res, err := db.Exec("DELETE FROM audit_events WHERE time_ms < ?", before)
	if err != nil {
		return 0, fmt.Errorf("清理过期审计事件失败: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// PruneRows 把审计表裁到最多 max 行（保留最新的），返回删除条数。
//
// 这是**兜底**：时间保留（retention.AuditDays）才是主口径。降级模式不做任何事——
// 它本来就只留最近 MaxEvents 条。
func (s *Store) PruneRows(max int) (int, error) {
	if max <= 0 {
		return 0, nil
	}
	s.mu.RLock()
	db := s.db
	s.mu.RUnlock()
	if db == nil {
		return 0, nil
	}
	res, err := db.Exec(`DELETE FROM audit_events WHERE id NOT IN (
		SELECT id FROM audit_events ORDER BY time_ms DESC, id DESC LIMIT ?)`, max)
	if err != nil {
		return 0, fmt.Errorf("裁剪审计事件失败: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
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
