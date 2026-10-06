package report

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/nebula/monitor/internal/server/config"
)

// 报告的周期化调度（全景表 11-4）。
//
// 为什么不做"通用定时任务"：平台里需要定时的只有报告与数据保留（后者已有自己的循环）。
// 为两条用例引入一套 cron 表达式解析 + 任务注册表，收益只是"看起来更通用"，
// 代价是多一套需要维护与排障的调度器。这里只做报告这一件事，但把该有的做全：
// 可关、可改周期、可立即执行、上次结果可见、失败显式上报。
//
// 与 Web 端手动生成的关系：手动生成走同一个 RunNow，因此"手动生成的报告"同样
// 会更新"上次运行"——否则界面上会出现"我明明刚生成过，调度却说到点该生成了"。

// ScheduleConfig 是调度配置。**同一个文件里同时存运行状态**（lastRunAt / lastReportId /
// lastError）：运行状态必须跨重启保留，否则频繁重启的机器每次启动都会重新生成一份报告
// （报告是重操作，会拉一整个周期的数据）。
type ScheduleConfig struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
	// Type 是报告类型：daily | weekly | monthly（与 ReportType 一致）。
	Type string `json:"type" yaml:"type"`
	// IntervalHours 是生成间隔（小时），1..720。
	IntervalHours int `json:"intervalHours" yaml:"intervalHours"`

	// ---- 运行状态（由调度器自己维护，界面上只读）----
	LastRunAt    int64  `json:"lastRunAt,omitempty" yaml:"lastRunAt,omitempty"`
	LastReportID string `json:"lastReportId,omitempty" yaml:"lastReportId,omitempty"`
	LastError    string `json:"lastError,omitempty" yaml:"lastError,omitempty"`
}

// ScheduleRun 是一次执行的结果。
type ScheduleRun struct {
	At       int64  `json:"at"`
	ReportID string `json:"reportId,omitempty"`
	Error    string `json:"error,omitempty"`
	Manual   bool   `json:"manual,omitempty"`
}

// ScheduleStatus 是调度现状（供 Web 端展示）。
type ScheduleStatus struct {
	Config ScheduleConfig `json:"config"`
	Last   *ScheduleRun   `json:"last,omitempty"`
	// NextAt 是下次预计生成时间（未启用时为 0）。
	NextAt int64 `json:"nextAt,omitempty"`
}

const (
	// DefaultScheduleIntervalHours 默认间隔：一天一份。
	DefaultScheduleIntervalHours = 24
	// MaxScheduleIntervalHours 上限 30 天：再长就不像"周期化"了，也容易让人忘了它开着。
	MaxScheduleIntervalHours = 720
	// scheduleTick 是检查间隔。用"每分钟看一次是否到点"而不是 time.Ticker(interval)：
	// 后者在改配置后不会生效，而"保存即热生效"是平台里所有 Web 可编辑配置的既有约定。
	scheduleTick = time.Minute
)

// Scheduler 周期化生成报告。
type Scheduler struct {
	mu   sync.RWMutex
	path string
	cfg  ScheduleConfig
	// generate 是生成报告的函数（默认 gen.Generate）。
	//
	// 抽成一个字段而不是直接持有 *Generator：调度要守的是"到点才生成 / 失败必须可见 /
	// 上次运行跨重启保留"，这些与报告内容无关；直接依赖具体生成器会让这些用例
	// 不得不先搭起一套时序库与节点管理器（否则生成期就崩了）。
	generate func(ReportType) (string, error)
	now      func() time.Time
}

// NewScheduler 创建调度器。配置文件不存在时用 initial 初始化并落盘（与保留策略一致）。
func NewScheduler(path string, initial ScheduleConfig, gen *Generator) (*Scheduler, error) {
	s := &Scheduler{path: path, generate: gen.Generate, now: time.Now}
	cfg := initial
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("解析报告调度配置失败: %w", err)
		}
	case errors.Is(err, os.ErrNotExist):
		if err := writeSchedule(path, cfg); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("读取报告调度配置失败: %w", err)
	}
	cfg.normalize()
	s.cfg = cfg
	return s, nil
}

// normalize 校正非法取值（而不是报错拒绝）：配置是人在 Web 上填的，
// 一个越界的间隔不该让整个调度失效——那会让"报告怎么不出了"变成一个谜。
func (c *ScheduleConfig) normalize() {
	switch ReportType(c.Type) {
	case ReportDaily, ReportWeekly, ReportMonthly:
	default:
		c.Type = string(ReportWeekly)
	}
	if c.IntervalHours <= 0 {
		c.IntervalHours = DefaultScheduleIntervalHours
	}
	if c.IntervalHours > MaxScheduleIntervalHours {
		c.IntervalHours = MaxScheduleIntervalHours
	}
}

// Config 返回当前配置（含运行状态）。
func (s *Scheduler) Config() ScheduleConfig {
	if s == nil {
		return ScheduleConfig{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// Save 保存配置（热生效）。运行状态字段由调度器维护，这里保留原值。
func (s *Scheduler) Save(cfg ScheduleConfig) error {
	if s == nil {
		return errors.New("报告调度未启用")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg.LastRunAt = s.cfg.LastRunAt
	cfg.LastReportID = s.cfg.LastReportID
	cfg.LastError = s.cfg.LastError
	cfg.normalize()
	if err := writeSchedule(s.path, cfg); err != nil {
		return err
	}
	s.cfg = cfg
	return nil
}

// Status 返回调度现状。
func (s *Scheduler) Status() ScheduleStatus {
	if s == nil {
		return ScheduleStatus{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := ScheduleStatus{Config: s.cfg}
	if s.cfg.LastRunAt > 0 {
		out.Last = &ScheduleRun{
			At:       s.cfg.LastRunAt,
			ReportID: s.cfg.LastReportID,
			Error:    s.cfg.LastError,
		}
		if s.cfg.Enabled {
			out.NextAt = s.cfg.LastRunAt + int64(s.cfg.IntervalHours)*int64(time.Hour/time.Millisecond)
		}
	}
	return out
}

// RunNow 立即生成一次报告并记录结果（manual=true 表示由 Web 端手动触发）。
//
// 生成失败**不 panic、不静默**：结果里带错误，同时写日志——否则"报告怎么不出了"
// 只能靠翻文件时间猜。
func (s *Scheduler) RunNow(manual bool) ScheduleRun {
	if s == nil {
		return ScheduleRun{Error: "报告调度未启用"}
	}
	cfg := s.Config()
	run := ScheduleRun{At: s.now().UnixMilli(), Manual: manual}
	id, err := s.generate(ReportType(cfg.Type))
	if err != nil {
		run.Error = err.Error()
		slog.Error("周期化报告生成失败", "type", cfg.Type, "err", err)
	} else {
		run.ReportID = id
	}

	s.mu.Lock()
	s.cfg.LastRunAt = run.At
	s.cfg.LastReportID = run.ReportID
	s.cfg.LastError = run.Error
	if err := writeSchedule(s.path, s.cfg); err != nil {
		// 落盘失败只影响"跨重启的去重"，不影响本次生成结果：记日志继续。
		slog.Warn("报告调度状态落盘失败（下次重启可能重复生成一次）", "err", err)
	}
	s.mu.Unlock()
	return run
}

// Run 按周期执行，直到 ctx 取消。
//
// 启动时**不**立即生成：报告要拉一整个周期的数据，重启就生成一份会在频繁重启的
// 机器上把磁盘写满；是否到点由 lastRunAt（跨重启保留）判断。
func (s *Scheduler) Run(ctx context.Context) {
	if s == nil {
		return
	}
	ticker := time.NewTicker(scheduleTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick()
		}
	}
}

// tick 检查是否到点，到点则生成一次。
func (s *Scheduler) tick() {
	cfg := s.Config()
	if !cfg.Enabled {
		return
	}
	interval := time.Duration(cfg.IntervalHours) * time.Hour
	if cfg.LastRunAt > 0 && s.now().Sub(time.UnixMilli(cfg.LastRunAt)) < interval {
		return
	}
	s.RunNow(false)
}

func writeSchedule(path string, cfg ScheduleConfig) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("序列化报告调度配置失败: %w", err)
	}
	if err := config.AtomicWrite(path, data); err != nil {
		return fmt.Errorf("保存报告调度配置失败: %w", err)
	}
	return nil
}
