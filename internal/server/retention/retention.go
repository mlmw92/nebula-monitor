// Package retention 管理本地数据的保留策略。
//
// 纳管范围（能真正清理的）：
//   - 告警处置记录：只清理「已处置（已认领/已关闭）」且超过保留期的记录，
//     待处理的记录一律保留——它们仍需要人处理，不能因时间久远被静默清掉；
//   - 巡检报告：按生成时间删除 HTML 文件并同步裁剪历史记录（两者必须一起处理，
//     否则会出现「历史里有条目但点开 404」或「文件永远留在磁盘上」）。
//
// 只读呈现（不尝试修改）：
//   - 时序库保留期由时序库自身的启动参数决定（VictoriaMetrics 的 -retentionPeriod），
//     Server 无法在运行期修改。这里探测并展示当前值，同时给出修改方式，
//     而不是假装纳管；
//   - 审计事件与安全事件由内置上限约束（内存与文件同步裁剪），因此不会无界增长，
//     仅展示当前条数与上限。
package retention

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/server/alert"
	"github.com/nebula/monitor/internal/server/audit"
	"github.com/nebula/monitor/internal/server/config"
	"github.com/nebula/monitor/internal/server/logstore"
	"github.com/nebula/monitor/internal/server/report"
	"github.com/nebula/monitor/internal/server/security"
	"gopkg.in/yaml.v3"
)

// 默认策略与边界。
const (
	// DefaultAcksDays 已处置告警记录的默认保留天数。
	DefaultAcksDays = 90
	// DefaultReportsDays 巡检报告的默认保留天数。
	DefaultReportsDays = 180
	// DefaultAuditDays 审计事件的默认保留天数。
	//
	// 取 180 天（与巡检报告同档）：入库之后审计的保留口径从「最近 2000 条」改成**时间**，
	// 意义就在"能查到多久以前"（设计件批次 18）。磁盘安全另由 audit.MaxRows 兜底。
	DefaultAuditDays = 180
	// DefaultLogsDays 集中日志的默认保留天数（C2）。
	// 取 7 天：日志量远大于其它类别，而「刚过去的这一周」覆盖了绝大多数排查场景；
	// 与 reports 的量级一致，避免「一个新开关悄悄吃掉磁盘」。
	DefaultLogsDays = 7
	// DefaultIntervalHours 自动清理的默认周期（小时）。
	DefaultIntervalHours = 24
	// minIntervalHours 自动清理周期下限，避免配置成 0 导致忙循环。
	minIntervalHours = 1
	// tsdbProbeTimeout 探测时序库保留参数的超时。
	tsdbProbeTimeout = 2 * time.Second
)

// Config 是本地数据保留策略（Web 端可改，保存即热生效）。
type Config struct {
	// Enabled 是否启用「自动」周期清理；手动「立即清理」不受该开关限制。
	Enabled bool `yaml:"enabled" json:"enabled"`
	// AcksDays 已处置告警记录的保留天数；0 表示不清理该类。
	AcksDays int `yaml:"acksDays" json:"acksDays"`
	// ReportsDays 巡检报告的保留天数；0 表示不清理该类。
	ReportsDays int `yaml:"reportsDays" json:"reportsDays"`
	// AuditDays 审计事件的保留天数；0 表示不清理该类。
	AuditDays int `yaml:"auditDays" json:"auditDays"`
	// LogsDays 集中日志的保留天数（C2）；0 表示不清理该类。
	LogsDays int `yaml:"logsDays" json:"logsDays"`
	// IntervalHours 自动清理周期（小时）。
	IntervalHours int `yaml:"intervalHours" json:"intervalHours"`
}

// DefaultConfig 返回默认保留策略。
func DefaultConfig() Config {
	return Config{
		Enabled:       true,
		AcksDays:      DefaultAcksDays,
		ReportsDays:   DefaultReportsDays,
		AuditDays:     DefaultAuditDays,
		LogsDays:      DefaultLogsDays,
		IntervalHours: DefaultIntervalHours,
	}
}

func (c *Config) normalize() {
	if c.AcksDays < 0 {
		c.AcksDays = 0
	}
	if c.ReportsDays < 0 {
		c.ReportsDays = 0
	}
	if c.LogsDays < 0 {
		c.LogsDays = 0
	}
	if c.AuditDays < 0 {
		c.AuditDays = 0
	}
	if c.IntervalHours < minIntervalHours {
		c.IntervalHours = DefaultIntervalHours
	}
}

// BuiltinLimit 描述由内置上限约束的数据类。
type BuiltinLimit struct {
	Count int `json:"count"`
	Cap   int `json:"cap"`
}

// TSDBRetention 描述时序库侧的保留配置（只读）。
type TSDBRetention struct {
	Addr    string `json:"addr"`
	Setting string `json:"setting,omitempty"`
	Error   string `json:"error,omitempty"`
}

// CleanupResult 是一次清理的结果。
type CleanupResult struct {
	At                   int64  `json:"at"`
	AcksRemoved          int    `json:"acksRemoved"`
	AcksCutoff           int64  `json:"acksCutoff,omitempty"`
	AuditRemoved         int    `json:"auditRemoved"`
	AuditCutoff          int64  `json:"auditCutoff,omitempty"`
	AuditRowsRemoved     int    `json:"auditRowsRemoved,omitempty"` // 兜底条数上限裁掉的条数
	ReportFilesRemoved   int    `json:"reportFilesRemoved"`
	ReportHistoryRemoved int    `json:"reportHistoryRemoved"`
	ReportsCutoff        int64  `json:"reportsCutoff,omitempty"`
	LogDirsRemoved       int    `json:"logDirsRemoved"`  // 集中日志：删除的日期分片数
	LogFilesRemoved      int    `json:"logFilesRemoved"` // 集中日志：删除的文件数
	LogsCutoff           int64  `json:"logsCutoff,omitempty"`
	FreedBytes           int64  `json:"freedBytes"`
	Skipped              string `json:"skipped,omitempty"` // 未执行清理的原因（如未接入对应存储）
	// Errors 是本次清理中失败的分项。清理失败**必须显式上报**：
	// 静默吞掉错误会让人看到一次「清理完成、删除 0 条」，而数据其实在无界增长。
	Errors []string `json:"errors,omitempty"`
}

// Status 是数据保留现状（供 Web 端展示）。
type Status struct {
	Config      Config             `json:"config"`
	Acks        alert.AckStats     `json:"acks"`
	Reports     report.ReportStats `json:"reports"`
	Logs        logstore.Stats     `json:"logs"`
	Audit       BuiltinLimit       `json:"audit"`
	Security    BuiltinLimit       `json:"security"`
	TSDB        TSDBRetention      `json:"tsdb"`
	LastCleanup *CleanupResult     `json:"lastCleanup,omitempty"`
	// LogsBackend 是集中日志的后端标识（local / victorialogs）。
	// 切到外部后端后 Logs 恒为零——那里的占用与保留由后端自己管。
	// 必须显式回传：否则"日志 0 文件 0 字节"看起来像日志功能坏了。
	LogsBackend string `json:"logsBackend,omitempty"`
}

// Manager 持有保留策略并执行清理。所有字段在构造后只读，故仅需保护配置与上次结果。
type Manager struct {
	mu   sync.RWMutex
	cfg  Config
	path string

	acks     *alert.AckStore
	reports  *report.Generator
	audit    *audit.Store
	security *security.Store
	tsdbAddr string

	// logs 为集中日志存储（C2，可空）。用注入而不是构造参数：
	// 它是本包最后纳入的一类数据，而构造参数已经很长——再加一个会迫使所有调用点（含测试）跟着改，
	// 收益却只有「少一行注入」。可选能力一律走 Set* 注入（与模板存储、日志存储的写法一致）。
	logs *logstore.Store

	// logsExternal 是"日志由外部后端接管"时的后端名（非空且 logs 为 nil 时生效）。
	// 保留期归外部后端，但清理结果与状态必须把这件事说出来（见 SetLogStoreExternal）。
	logsExternal string

	lastMu sync.Mutex
	last   *CleanupResult
}

// New 创建保留策略管理器：配置文件不存在时用 initial 初始化并落盘（与其它 Web 可编辑配置一致）。
func New(path string, initial Config, acks *alert.AckStore, reports *report.Generator,
	auditStore *audit.Store, sec *security.Store, tsdbAddr string) (*Manager, error) {
	initial.normalize()
	m := &Manager{
		cfg: initial, path: path, acks: acks, reports: reports,
		audit: auditStore, security: sec, tsdbAddr: tsdbAddr,
	}
	data, err := os.ReadFile(path)
	if err != nil {
		// 首次运行：落盘默认值，便于运维直接在文件里看到可调项
		if err := m.Save(initial); err != nil {
			return nil, fmt.Errorf("初始化数据保留配置失败: %w", err)
		}
		return m, nil
	}
	// 从**默认值**起解，而不是从零值起：否则配置文件里**没有**的字段会解成 0，而 0 在本
	// 配置里的含义是「不清理该类」——新增一个保留类（本批的 auditDays、C2 的 logsDays）后，
	// 存量部署会静默地永远不清理它，看起来像"清理跑了但什么都没做"。
	// 显式写 0 仍然是"不清理"，语义不变。
	cfg := initial
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析数据保留配置失败: %w", err)
	}
	cfg.normalize()
	m.cfg = cfg
	return m, nil
}

// SetLogStore 注入集中日志存储（C2；未注入时该类不参与清理与统计）。
func (m *Manager) SetLogStore(s *logstore.Store) {
	if m != nil {
		m.logs = s
	}
}

// SetLogStoreExternal 声明集中日志由**外部后端**接管（此时不注入本地存储）。
//
// 外部后端自管保留期（VictoriaLogs 的 -retentionPeriod）：平台既不该也不能去删它的数据。
// 但要**说出来**——否则界面会把"日志 0 文件"显示成"日志未接入"，
// 把一次有意的架构选择说成配置缺失。
func (m *Manager) SetLogStoreExternal(backend string) {
	if m != nil {
		m.logsExternal = backend
	}
}

// logsBackend 返回日志后端标识：注入了本地存储就是 local，被外部接管就是后端名。
func (m *Manager) logsBackend() string {
	if m.logs != nil {
		return m.logs.Backend()
	}
	return m.logsExternal
}

// Config 返回当前策略。
func (m *Manager) Config() Config {
	if m == nil {
		return DefaultConfig()
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

// Save 持久化并热生效保留策略。
func (m *Manager) Save(cfg Config) error {
	cfg.normalize()
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := config.AtomicWrite(m.path, data); err != nil {
		return err
	}
	m.mu.Lock()
	m.cfg = cfg
	m.mu.Unlock()
	return nil
}

// Status 汇总当前保留现状（含时序库只读探测）。
func (m *Manager) Status() Status {
	if m == nil {
		return Status{Config: DefaultConfig()}
	}
	out := Status{Config: m.Config()}
	out.LogsBackend = m.logsBackend()
	if m.acks != nil {
		out.Acks = m.acks.Stats()
	}
	if m.reports != nil {
		out.Reports = m.reports.Stats()
	}
	if m.audit != nil {
		// 入库后审计的保留主口径是**时间**（config.auditDays），这里的 Cap 是兜底条数上限。
		out.Audit = BuiltinLimit{Count: m.audit.Count(), Cap: audit.MaxRows}
	}
	if m.security != nil {
		out.Security = BuiltinLimit{Count: m.security.Count(), Cap: security.MaxEvents}
	}
	// 集中日志的实际占用：这是运维开关日志前最想知道的事（「一天到底占多少盘」）
	out.Logs = m.logs.Stats()
	out.TSDB = TSDBRetention{Addr: m.tsdbAddr}
	if setting, err := ProbeTSDBRetention(m.tsdbAddr); err != nil {
		out.TSDB.Error = err.Error()
	} else {
		out.TSDB.Setting = setting
	}
	out.LastCleanup = m.lastResult()
	return out
}

// CleanupNow 按当前策略立即清理一次（不受 Enabled 限制）。
func (m *Manager) CleanupNow() CleanupResult {
	if m == nil {
		return CleanupResult{At: time.Now().UnixMilli(), Skipped: "保留策略未启用"}
	}
	return m.cleanup(m.Config())
}

func (m *Manager) cleanup(cfg Config) CleanupResult {
	res := CleanupResult{At: time.Now().UnixMilli()}
	now := time.Now()
	// 「有没有真正可做的事」比「天数是否为 0」更准确：天数 > 0 但对应数据源没接入时，
	// 实际什么都不会发生——此时不给原因，会让人以为清理已经跑过了。
	actionable := (cfg.AcksDays > 0 && m.acks != nil) ||
		(cfg.ReportsDays > 0 && m.reports != nil) ||
		(cfg.LogsDays > 0 && m.logs != nil) ||
		(cfg.AuditDays > 0 && m.audit != nil)
	switch {
	case m.acks == nil && m.reports == nil && m.logs == nil && m.audit == nil && m.logsExternal == "":
		res.Skipped = "未接入可清理的数据源"
	case !actionable && m.logsExternal != "":
		// 日志由外部后端接管时，"没做什么"是**有意的**：保留期归那个后端。
		// 若只说"未接入数据源"，会把一次架构选择说成配置缺失。
		res.Skipped = "集中日志由外部后端（" + m.logsExternal + "）负责保留与容量，平台不执行日志清理"
	case !actionable:
		res.Skipped = "保留天数均为 0 或对应数据源未接入（无可清理内容）"
	}
	if cfg.AcksDays > 0 && m.acks != nil {
		cutoff := now.AddDate(0, 0, -cfg.AcksDays)
		res.AcksCutoff = cutoff.UnixMilli()
		if removed, err := m.acks.PruneHandled(cutoff.UnixMilli()); err != nil {
			// 清理失败不能假装成功：本次一条都没删，且必须让调用方与日志都看得见。
			res.Errors = append(res.Errors, "告警处置清理失败: "+err.Error())
			slog.Warn("告警处置清理失败", "err", err)
		} else {
			res.AcksRemoved = removed
		}
	}
	if cfg.AuditDays > 0 && m.audit != nil {
		cutoff := now.AddDate(0, 0, -cfg.AuditDays)
		res.AuditCutoff = cutoff.UnixMilli()
		if removed, err := m.audit.Prune(cutoff.UnixMilli()); err != nil {
			res.Errors = append(res.Errors, "审计事件清理失败: "+err.Error())
			slog.Warn("审计事件清理失败", "err", err)
		} else {
			res.AuditRemoved = removed
		}
		// 兜底条数：时间口径是主规则，条数是"单机磁盘被写爆"的安全网。
		if rows, err := m.audit.PruneRows(audit.MaxRows); err != nil {
			res.Errors = append(res.Errors, "审计条数裁剪失败: "+err.Error())
			slog.Warn("审计条数裁剪失败", "err", err)
		} else {
			res.AuditRowsRemoved = rows
		}
	}
	if cfg.ReportsDays > 0 && m.reports != nil {
		cutoff := now.AddDate(0, 0, -cfg.ReportsDays)
		res.ReportsCutoff = cutoff.UnixMilli()
		pruned := m.reports.PruneBefore(cutoff)
		res.ReportFilesRemoved = pruned.Files
		res.ReportHistoryRemoved = pruned.History
		res.FreedBytes += pruned.Bytes
	}
	if cfg.LogsDays > 0 && m.logs != nil {
		cutoff := now.AddDate(0, 0, -cfg.LogsDays)
		res.LogsCutoff = cutoff.UnixMilli()
		pruned := m.logs.PruneBefore(cutoff)
		res.LogDirsRemoved = pruned.DirsRemoved
		res.LogFilesRemoved = pruned.FilesRemoved
		res.FreedBytes += pruned.Bytes
	}
	m.setLast(res)
	return res
}

// Run 按周期执行自动清理，直到 ctx 取消。启动时先执行一次，避免重启后长期不清理。
func (m *Manager) Run(ctx context.Context) {
	if m == nil {
		return
	}
	if cfg := m.Config(); cfg.Enabled {
		res := m.cleanup(cfg)
		slog.Info("数据保留清理完成", "acksRemoved", res.AcksRemoved,
			"reportFiles", res.ReportFilesRemoved, "reportHistory", res.ReportHistoryRemoved, "freedBytes", res.FreedBytes)
	}
	for {
		timer := time.NewTimer(time.Duration(m.Config().IntervalHours) * time.Hour)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			cfg := m.Config()
			if !cfg.Enabled {
				continue
			}
			res := m.cleanup(cfg)
			slog.Info("数据保留清理完成", "acksRemoved", res.AcksRemoved,
				"reportFiles", res.ReportFilesRemoved, "reportHistory", res.ReportHistoryRemoved, "freedBytes", res.FreedBytes)
		}
	}
}

func (m *Manager) setLast(res CleanupResult) {
	m.lastMu.Lock()
	defer m.lastMu.Unlock()
	m.last = &res
}

func (m *Manager) lastResult() *CleanupResult {
	m.lastMu.Lock()
	defer m.lastMu.Unlock()
	return m.last
}

// ProbeTSDBRetention 只读探测时序库的保留参数（VictoriaMetrics 系列暴露 /flags）。
//
// 返回 (参数描述, 错误)。错误表示无法读取，调用方应提示「需在时序库侧配置」——
// 指标保留由时序库自身启动参数决定，Server 无法在运行期修改。
func ProbeTSDBRetention(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", fmt.Errorf("未配置时序库地址")
	}
	client := &http.Client{Timeout: tsdbProbeTimeout}
	resp, err := client.Get(strings.TrimRight(addr, "/") + "/flags")
	if err != nil {
		return "", fmt.Errorf("无法连接时序库: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("时序库返回 HTTP %d", resp.StatusCode)
	}
	var flags map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&flags); err != nil {
		return "", fmt.Errorf("解析 /flags 失败: %v", err)
	}
	for key, value := range flags {
		if strings.HasSuffix(key, "retentionPeriod") || strings.HasSuffix(key, "retentionFilter") {
			return key + "=" + value, nil
		}
	}
	return "", fmt.Errorf("时序库未显式暴露保留参数，当前保留期由其后端默认值决定")
}
