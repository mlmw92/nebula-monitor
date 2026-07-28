// Command agent 是被监控节点上运行的轻量级采集上报进程。
package main

import (
	"flag"
	"log/slog"
	"os"
	"time"

	"github.com/nebula/monitor/internal/agent/collector"
	"github.com/nebula/monitor/internal/agent/config"
	"github.com/nebula/monitor/internal/agent/reporter"
	"github.com/nebula/monitor/internal/agent/upgrader"
	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/version"
)

func main() {
	cfgPath := flag.String("config", "agent.yaml", "配置文件路径")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		slog.Error("加载配置失败", "err", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	coll := collector.New(cfg.Node, cfg.Group, cfg.Labels, cfg.Collectors)
	rep := reporter.New(cfg.ServerURL, cfg.Node, cfg.Group, cfg.Secret, cfg.Labels)

	slog.Info("Agent 启动", "node", cfg.Node, "server", cfg.ServerURL, "interval", cfg.Interval, "version", version.Version)

	ticker := time.NewTicker(time.Duration(cfg.Interval) * time.Second)
	defer ticker.Stop()

	// 立即采集一次
	collectAndReport(coll, rep, cfg)

	for range ticker.C {
		collectAndReport(coll, rep, cfg)
	}
}

func collectAndReport(coll *collector.Collector, rep *reporter.Reporter, cfg *config.Config) {
	metrics, procs := coll.Collect()
	osName, arch, ip := coll.HostInfo()
	payload := model.ReportPayload{
		Node:      cfg.Node,
		IP:        ip,
		OS:        osName,
		Arch:      arch,
		Group:     cfg.Group,
		Labels:    cfg.Labels,
		Version:   version.Version,
		HostInfo:  collector.CollectHostInfo(),
		Metrics:   metrics,
		Processes: procs,
		ReportAt:  model.NowMillis(),
	}
	resp, err := rep.ReportFull(payload)
	if err != nil {
		// 错误已在 reporter 内记录，这里仅跳过本轮
		return
	}
	slog.Debug("上报成功", "metrics", len(metrics), "procs", len(procs))

	// 检查 Server 下发的指令
	if resp.Command == "upgrade" {
		slog.Info("收到升级指令，开始自升级流程", "server", cfg.ServerURL)
		upgrader.Run(cfg)
		// 等待脚本接管后退出当前进程，systemd 会拉起新版本
		time.Sleep(3 * time.Second)
		os.Exit(0)
	}
}
