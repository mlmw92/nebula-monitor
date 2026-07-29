// Command agent 是被监控节点上运行的轻量级采集上报进程。
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/nebula/monitor/internal/agent/collector"
	"github.com/nebula/monitor/internal/agent/config"
	"github.com/nebula/monitor/internal/agent/reporter"
	"github.com/nebula/monitor/internal/agent/upgrader"
	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/version"
)

const pidFile = "/var/run/monitor-agent.pid"

func main() {
	cfgPath := flag.String("config", "agent.yaml", "配置文件路径")
	showVersion := flag.Bool("version", false, "显示版本信息")
	flag.Parse()
	if *showVersion {
		fmt.Printf("nebula-monitor agent %s\n", version.Version)
		fmt.Printf("build_time=%s\n", version.BuildTime)
		fmt.Printf("go_version=%s\n", version.GoVersion)
		return
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		slog.Error("加载配置失败", "err", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	// 写 pid 文件，升级脚本通过它等待 agent 退出
	_ = os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o644)
	defer os.Remove(pidFile)

	coll := collector.New(cfg.Node, cfg.Group, cfg.Labels, cfg.Collectors, cfg.RedisInstances)
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
	redisMetrics, redisInstances := coll.CollectRedis()
	if len(redisMetrics) > 0 {
		metrics = append(metrics, redisMetrics...)
	}
	osName, arch, ip := coll.HostInfo()
	payload := model.ReportPayload{
		Node:           cfg.Node,
		IP:             ip,
		OS:             osName,
		Arch:           arch,
		Group:          cfg.Group,
		Labels:         cfg.Labels,
		Version:        version.Version,
		HostInfo:       collector.CollectHostInfo(),
		Metrics:        metrics,
		Processes:      procs,
		RedisInstances: redisInstances,
		ReportAt:       model.NowMillis(),
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
		// 不主动退出：升级脚本会用 systemctl stop 停止本进程。
		// 阻塞等待，避免 ticker 再次触发上报导致 ConsumeUpgrade 已消费但升级未完成。
		slog.Info("等待升级脚本执行，agent 进入阻塞状态")
		select {}
	}
}
