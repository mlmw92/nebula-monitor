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
	// IntervalHours 自动清理周期（小时）。
	IntervalHours int `yaml:"intervalHours" json:"intervalHours"`
}

// DefaultConfig 返回默认保留策略。
func DefaultConfig() Config {
	return Config{Enabled: true, AcksDays: DefaultAcksDays, ReportsDays: DefaultReportsDays, IntervalHours: DefaultIntervalHours}
}

func (c *Config) normalize() {
	if c.AcksDays < 0 {
		c.AcksDays = 0
	}
	if c.ReportsDays < 0 {
		c.ReportsDays = 0
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
	ReportFilesRemoved   int    `json:"reportFilesRemoved"`
	ReportHistoryRemoved int    `json:"reportHistoryRemoved"`
	ReportsCutoff        int64  `json:"reportsCutoff,omitempty"`
	FreedBytes           int64  `json:"freedBytes"`
	Skipped              string `json:"skipped,omitempty"` // 未执行清理的原因（如未接入对应存储）
}

// Status 是数据保留现状（供 Web 端展示）。
type Status struct {
	Config      Config             `json:"config"`
	Acks        alert.AckStats     `json:"acks"`
	Reports     report.ReportStats `json:"reports"`
	Audit       BuiltinLimit       `json:"audit"`
	Security    BuiltinLimit       `json:"security"`
	TSDB        TSDBRetention      `json:"tsdb"`
	LastCleanup *CleanupResult     `json:"lastCleanup,omitempty"`
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
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析数据保留配置失败: %w", err)
	}
	cfg.normalize()
	m.cfg = cfg
	return m, nil
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
	if m.acks != nil {
		out.Acks = m.acks.Stats()
	}
	if m.reports != nil {
		out.Reports = m.reports.Stats()
	}
	if m.audit != nil {
		out.Audit = BuiltinLimit{Count: m.audit.Count(), Cap: audit.MaxEvents}
	}
	if m.security != nil {
		out.Security = BuiltinLimit{Count: m.security.Count(), Cap: security.MaxEvents}
	}
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
	switch {
	case m.acks == nil && m.reports == nil:
		res.Skipped = "未接入可清理的数据源"
	case cfg.AcksDays <= 0 && cfg.ReportsDays <= 0:
		res.Skipped = "保留天数均为 0（表示不清理）"
	}
	if cfg.AcksDays > 0 && m.acks != nil {
		cutoff := now.AddDate(0, 0, -cfg.AcksDays)
		res.AcksCutoff = cutoff.UnixMilli()
		res.AcksRemoved = m.acks.PruneHandled(cutoff.UnixMilli())
	}
	if cfg.ReportsDays > 0 && m.reports != nil {
		cutoff := now.AddDate(0, 0, -cfg.ReportsDays)
		res.ReportsCutoff = cutoff.UnixMilli()
		pruned := m.reports.PruneBefore(cutoff)
		res.ReportFilesRemoved = pruned.Files
		res.ReportHistoryRemoved = pruned.History
		res.FreedBytes = pruned.Bytes
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
