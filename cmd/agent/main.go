// Command agent 是被监控节点上运行的轻量级采集上报进程。
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/nebula/monitor/internal/agent/collector"
	"github.com/nebula/monitor/internal/agent/config"
	"github.com/nebula/monitor/internal/agent/proxy"
	"github.com/nebula/monitor/internal/agent/reporter"
	"github.com/nebula/monitor/internal/agent/upgrader"
	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/version"
)

const pidFile = "/var/run/monitor-agent.pid"

// agentBinSHA 是当前 agent 二进制的 SHA256，随上报提交给 Server，
// 作为升级成功的确认依据（与版本号解耦：CDN 里的二进制是什么，目标就是什么）。
var agentBinSHA = binSHA256()

// binSHA256 计算当前 agent 二进制的 SHA256（十六进制小写）。
func binSHA256() string {
	path, err := os.Executable()
	if err != nil {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// watchUpgradeSignal 监控升级信号文件：非 systemd 环境下升级脚本下载校验完成后
// 写入该文件，本进程检测到后退出，交由脚本替换二进制并重新拉起。
func watchUpgradeSignal() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if _, err := os.Stat(upgrader.ReadyFile); err == nil {
			slog.Info("检测到升级信号文件，退出进程等待替换")
			_ = os.Remove(pidFile)
			os.Exit(0)
		}
	}
}

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

	// 代理模式（edge/hub）走独立启动路径，不进入采集主循环
	if cfg.Mode == config.ModeEdge || cfg.Mode == config.ModeHub {
		runProxy(cfg)
		return
	}

	// 写 pid 文件，升级脚本通过它等待 agent 退出
	_ = os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o644)
	defer os.Remove(pidFile)

	// 清理可能残留的升级信号文件（上次非 systemd 升级中断遗留）
	_ = os.Remove(upgrader.ReadyFile)
	// 监控升级信号文件：非 systemd 环境下升级脚本写入后，本进程自行退出以便替换二进制
	go watchUpgradeSignal()

	coll := collector.New(cfg.Node, cfg.Group, cfg.Labels, cfg.Collectors,
		cfg.RedisInstances, cfg.MySQLInstances, cfg.PostgresInstances,
		cfg.NginxInstances, cfg.KafkaInstances, cfg.DockerInstances,
		cfg.RocketMQInstances, cfg.K8sInstances, cfg.PortChecks,
	)
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

// runProxy 启动代理模式（edge/hub），阻塞直至收到 SIGINT/SIGTERM。
func runProxy(cfg *config.Config) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 信号监听：SIGINT/SIGTERM 优雅退出
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		slog.Info("收到退出信号", "signal", sig)
		cancel()
	}()

	switch cfg.Mode {
	case config.ModeEdge:
		slog.Info("Agent 启动（Edge 代理模式）", "listen", cfg.Proxy.Listen, "hub", cfg.Proxy.HubAddr, "version", version.Version)
		edge, err := proxy.NewEdge(proxy.EdgeCfgFromConfig(cfg.Proxy))
		if err != nil {
			slog.Error("Edge 初始化失败", "err", err)
			os.Exit(1)
		}
		// 后台周期上报 Edge 自监控指标（复用 reporter）
		go reportProxyMetrics(cfg, edge)
		if err := edge.Run(ctx); err != nil {
			slog.Error("Edge 运行失败", "err", err)
			os.Exit(1)
		}
	case config.ModeHub:
		slog.Info("Agent 启动（Hub 代理模式）", "listen", cfg.Proxy.Listen, "server", cfg.Proxy.ServerURL, "version", version.Version)
		hub, err := proxy.NewHub(proxy.HubCfgFromConfig(cfg.Proxy), cfg.Proxy.ServerURL)
		if err != nil {
			slog.Error("Hub 初始化失败", "err", err)
			os.Exit(1)
		}
		go reportProxyMetrics(cfg, hub)
		if err := hub.Run(ctx); err != nil {
			slog.Error("Hub 运行失败", "err", err)
			os.Exit(1)
		}
	}
}

// proxyMetricsProvider 由 edge/hub 实现，提供自监控指标快照。
type proxyMetricsProvider interface {
	Metrics() proxy.MetricsSnapshot
}

// reportProxyMetrics 周期上报代理自监控指标到 Server。
// 上报格式复用现有 /api/v1/report，构造最小 ReportPayload 只含 proxy_* 指标。
func reportProxyMetrics(cfg *config.Config, p proxyMetricsProvider) {
	if cfg.ServerURL == "" && cfg.Mode == config.ModeHub {
		// Hub 的 ServerURL 是真实 Server，可直接上报
	}
	rep := reporter.New(cfg.ServerURL, cfg.Node, cfg.Group, cfg.Secret, cfg.Labels)
	node := cfg.Node
	if node == "" {
		node, _ = os.Hostname()
	}
	ticker := time.NewTicker(time.Duration(cfg.Interval) * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		m := p.Metrics()
		metrics := []model.Metric{
			{Name: "proxy_conn_active", Node: node, Value: float64(m.ConnActive), Timestamp: model.NowMillis()},
			{Name: "proxy_forward_total", Node: node, Value: float64(m.ForwardTotal), Timestamp: model.NowMillis()},
			{Name: "proxy_dropped_total", Node: node, Value: float64(m.DroppedTotal), Timestamp: model.NowMillis()},
			{Name: "proxy_reconnect_total", Node: node, Value: float64(m.ReconnectTotal), Timestamp: model.NowMillis()},
			{Name: "proxy_buffer_depth", Node: node, Value: float64(m.BufferDepth), Timestamp: model.NowMillis()},
		}
		payload := model.ReportPayload{
			Node:    node,
			Group:   cfg.Group,
			Labels:  cfg.Labels,
			Version: version.Version,
			Metrics: metrics,
		}
		if _, err := rep.ReportFull(payload); err != nil {
			slog.Debug("代理指标上报失败", "err", err)
		}
	}
}

func collectAndReport(coll *collector.Collector, rep *reporter.Reporter, cfg *config.Config) {
	metrics, procs := coll.Collect()

	// 中间件采集
	redisMetrics, redisInstances := coll.CollectRedis()
	metrics = append(metrics, redisMetrics...)
	mysqlMetrics, mysqlInstances := coll.CollectMySQL()
	metrics = append(metrics, mysqlMetrics...)
	pgMetrics, pgInstances := coll.CollectPostgres()
	metrics = append(metrics, pgMetrics...)
	nginxMetrics, nginxInstances := coll.CollectNginx()
	metrics = append(metrics, nginxMetrics...)
	kafkaMetrics, kafkaInstances := coll.CollectKafka()
	metrics = append(metrics, kafkaMetrics...)
	dockerMetrics, dockerInstances := coll.CollectDocker()
	metrics = append(metrics, dockerMetrics...)
	rmqMetrics, rmqInstances := coll.CollectRocketMQ()
	metrics = append(metrics, rmqMetrics...)
	k8sMetrics, k8sInstances := coll.CollectK8s()
	metrics = append(metrics, k8sMetrics...)

	osName, arch, ip := coll.HostInfo()
	payload := model.ReportPayload{
		Node:              cfg.Node,
		IP:                ip,
		OS:                osName,
		Arch:              arch,
		Group:             cfg.Group,
		Labels:            cfg.Labels,
		Version:           version.Version,
		BinSHA256:         agentBinSHA,
		HostInfo:          collector.CollectHostInfo(),
		Metrics:           metrics,
		Processes:         procs,
		RedisInstances:    redisInstances,
		MySQLInstances:    mysqlInstances,
		PostgresInstances: pgInstances,
		NginxInstances:    nginxInstances,
		KafkaInstances:    kafkaInstances,
		DockerInstances:   dockerInstances,
		RocketMQInstances: rmqInstances,
		K8sInstances:      k8sInstances,
		ReportAt:          model.NowMillis(),
	}
	resp, err := rep.ReportFull(payload)
	if err != nil {
		// 错误已在 reporter 内记录，这里仅跳过本轮
		return
	}
	slog.Debug("上报成功", "metrics", len(metrics), "procs", len(procs))

	// 检查 Server 下发的指令
	if resp.Command == "upgrade" {
		slog.Info("收到升级指令，启动自升级流程", "server", cfg.ServerURL)
		upgrader.Run(cfg)
		// 不阻塞：升级脚本先下载并校验新二进制（期间 agent 继续正常运行与上报），
		// 准备就绪后通过 systemctl stop 或升级信号文件停止本进程再替换。
		// 若脚本失败，agent 保持运行，等待 Server 下次心跳重试，不会假死。
		slog.Debug("自升级已在后台执行，agent 继续运行")
	}
}
