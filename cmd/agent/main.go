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
	"github.com/nebula/monitor/internal/agent/defense"
	"github.com/nebula/monitor/internal/agent/logship"
	opsagent "github.com/nebula/monitor/internal/agent/ops"
	"github.com/nebula/monitor/internal/agent/proxy"
	"github.com/nebula/monitor/internal/agent/reporter"
	"github.com/nebula/monitor/internal/agent/upgrader"
	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/version"
)

// pidFile Agent 进程 PID 文件路径。
const pidFile = "/var/run/monitor-agent.pid"

// opsExecutedPath 是下行操作执行记录的落盘路径（幂等：同一任务不重复执行）。
// 与 defense 的状态文件同处一棵目录树（/var/lib/nebula-monitor）。
const opsExecutedPath = "/var/lib/nebula-monitor/ops/executed.json"

// 受控 fail2ban 入侵防御相关全局组件（agent 单例）。
var (
	// defenseExec 受控 fail2ban 入侵防御执行器单例。
	defenseExec = defense.NewExecutor()
	// banCollector 封禁事件采集器单例。
	banCollector = defense.NewBanEventCollector()
	// pendingResult 已执行指令的结果，将在下一次 report 带回服务端。
	pendingResult *model.DefenseCommandResult
	// opsExec 统一下行操作执行器（在 main 中按配置的本机护栏创建）。
	opsExec *opsagent.Executor
	// pendingOpsResult 已执行操作任务的结果，将在下一次 report 带回服务端。
	pendingOpsResult *model.OpsResult
)

// agentBinSHA 是当前 agent 二进制的 SHA256，随上报提交给 Server，
// 作为升级成功的确认依据（与版本号解耦：CDN 里的二进制是什么，目标就是什么）。
var agentBinSHA = binSHA256()

// opsSupported 返回本机放行的下行动作清单；执行器未初始化（如代理模式）时返回空。
func opsSupported() []string {
	if opsExec == nil {
		return nil
	}
	return opsExec.Supported()
}

// binSHA256 计算当前 agent 二进制的 SHA256（十六进制小写）。
// logSourceIDs 取日志来源 id 清单（上报给 Server，用于校验上行日志的来源是否属于本节点）。
func logSourceIDs(srcs []config.LogSourceConfig) []string {
	if len(srcs) == 0 {
		return nil
	}
	out := make([]string, 0, len(srcs))
	for _, s := range srcs {
		out = append(out, s.ID)
	}
	return out
}

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

	// 统一下行操作执行器：本机护栏决定"这台机器愿意执行什么"，能力声明即由此产生。
	// 启动时把放行情况打进日志——排查"为什么下发被拒绝"时，第一步就是看这行。
	nodeName := cfg.Node
	if nodeName == "" {
		if h, err := os.Hostname(); err == nil {
			nodeName = h
		}
	}
	opsExec = opsagent.New(nodeName, cfg.Guards.Ops, opsExecutedPath)
	// 这里只报"本机放行了什么"，**刻意不报 supported**：supported 还取决于后面才注入的
	// 容器查询实现（coll.K8s()），在这一行打印会恒缺 container.*，
	// 排查"为什么容器动作被拒"时会被这行带偏。完整清单在注入之后单独打印。
	slog.Info("下行操作本机护栏已就绪",
		"readOnly", cfg.Guards.Ops.OpsReadOnlyEnabled(),
		"write", cfg.Guards.Ops.Write,
		"units", cfg.Guards.Ops.Units)

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
		cfg.RocketMQInstances, cfg.K8sInstances, cfg.MongoDBInstances, cfg.FastDFSInstances,
		cfg.RabbitMQInstances, cfg.ElasticsearchInstances, cfg.ClickHouseInstances,
		cfg.NacosInstances, cfg.ZooKeeperInstances,
		cfg.PortChecks, cfg.Security,
		time.Duration(cfg.CollectTimeout)*time.Second,
		cfg.LogSources, cfg.LogOffsetsFile,
	)
	// 集中日志（C2）：配置了 logSources 才构造上行器并接上采集器。
	// 未配置时既不构造也不注入，与改造前完全等价。
	if len(cfg.LogSources) > 0 {
		coll.SetLogSink(logship.New(cfg.ServerURL, cfg.Secret, coll.NodeName(), cfg.Group).Sink())
	}
	// 容器只读查询：把本机 K8s 采集器（已持有 k8sInstances 与凭据解析）接到下行通道上。
	// 未配集群时 coll.K8s() 为 nil，容器类动作因此不进能力清单——
	// 界面上显示"该节点不支持"，好过下发一条注定失败的任务。
	if k := coll.K8s(); k != nil {
		opsExec.SetK8sQuerier(k)
	}
	// 到这一行能力清单才完整：Node/服务类动作来自本机护栏，容器类动作取决于上面这次注入。
	// 单独一条而不是并进上面那句，是为了让代理模式（在注入之前就 return）保持原有日志行为。
	slog.Info("下行操作能力清单已就绪", "supported", opsExec.Supported())
	rep := reporter.New(cfg.ServerURL, cfg.Node, cfg.Group, cfg.Secret, cfg.Labels)

	// 构建已开启的采集器列表
	var enabledCollectors []string
	cs := cfg.Collectors
	if cs.CPU {
		enabledCollectors = append(enabledCollectors, "cpu")
	}
	if cs.Memory {
		enabledCollectors = append(enabledCollectors, "memory")
	}
	if cs.Disk {
		enabledCollectors = append(enabledCollectors, "disk")
	}
	if cs.Network {
		enabledCollectors = append(enabledCollectors, "network")
	}
	if cs.Process {
		enabledCollectors = append(enabledCollectors, "process")
	}
	if cs.Load {
		enabledCollectors = append(enabledCollectors, "load")
	}
	if cs.Redis {
		enabledCollectors = append(enabledCollectors, "redis")
	}
	if cs.MySQL {
		enabledCollectors = append(enabledCollectors, "mysql")
	}
	if cs.Postgres {
		enabledCollectors = append(enabledCollectors, "postgres")
	}
	if cs.Nginx {
		enabledCollectors = append(enabledCollectors, "nginx")
	}
	if cs.NginxLog {
		enabledCollectors = append(enabledCollectors, "nginxLog")
	}
	if cs.Kafka {
		enabledCollectors = append(enabledCollectors, "kafka")
	}
	if cs.Docker {
		enabledCollectors = append(enabledCollectors, "docker")
	}
	if cs.RocketMQ {
		enabledCollectors = append(enabledCollectors, "rocketmq")
	}
	if cs.K8s {
		enabledCollectors = append(enabledCollectors, "k8s")
	}
	if cs.MongoDB {
		enabledCollectors = append(enabledCollectors, "mongodb")
	}
	if cs.FastDFS {
		enabledCollectors = append(enabledCollectors, "fastdfs")
	}
	if cs.RabbitMQ {
		enabledCollectors = append(enabledCollectors, "rabbitmq")
	}
	if cs.Elasticsearch {
		enabledCollectors = append(enabledCollectors, "elasticsearch")
	}
	if cs.ClickHouse {
		enabledCollectors = append(enabledCollectors, "clickhouse")
	}
	if cs.Nacos {
		enabledCollectors = append(enabledCollectors, "nacos")
	}
	if cs.ZooKeeper {
		enabledCollectors = append(enabledCollectors, "zookeeper")
	}
	if cs.Port {
		enabledCollectors = append(enabledCollectors, "port")
	}
	if cs.Security {
		enabledCollectors = append(enabledCollectors, "security")
	}

	slog.Info("Agent 启动", "node", cfg.Node, "server", cfg.ServerURL, "interval", cfg.Interval, "version", version.Version, "collectors", enabledCollectors)

	ticker := time.NewTicker(time.Duration(cfg.Interval) * time.Second)
	defer ticker.Stop()

	// 采集任务上下文：进程生命周期内长期有效；每个采集任务再各自叠加 collectTimeout。
	ctx := context.Background()

	// 立即采集一次
	collectAndReport(ctx, coll, rep, cfg)

	for range ticker.C {
		collectAndReport(ctx, coll, rep, cfg)
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

// modeForModel 将 config 的运行模式转换为 model 常量。
func modeForModel(cfg *config.Config) string {
	switch cfg.Mode {
	case config.ModeEdge:
		return model.ModeEdge
	case config.ModeHub:
		return model.ModeHub
	default:
		return model.ModeCollect
	}
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
	mode := modeForModel(cfg)
	for range ticker.C {
		m := p.Metrics()
		metrics := []model.Metric{
			{Name: "proxy_conn_active", Node: node, Labels: map[string]string{"mode": mode}, Value: float64(m.ConnActive), Timestamp: model.NowMillis()},
			{Name: "proxy_forward_total", Node: node, Labels: map[string]string{"mode": mode}, Value: float64(m.ForwardTotal), Timestamp: model.NowMillis()},
			{Name: "proxy_dropped_total", Node: node, Labels: map[string]string{"mode": mode}, Value: float64(m.DroppedTotal), Timestamp: model.NowMillis()},
			{Name: "proxy_reconnect_total", Node: node, Labels: map[string]string{"mode": mode}, Value: float64(m.ReconnectTotal), Timestamp: model.NowMillis()},
			{Name: "proxy_buffer_depth", Node: node, Labels: map[string]string{"mode": mode}, Value: float64(m.BufferDepth), Timestamp: model.NowMillis()},
		}
		payload := model.ReportPayload{
			Node:    node,
			Mode:    mode,
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

func collectAndReport(ctx context.Context, coll *collector.Collector, rep *reporter.Reporter, cfg *config.Config) {
	// 一轮采集：各来源并发执行、各自独立超时（collectTimeout），互不阻塞。
	res := coll.CollectAll(ctx)

	// 采集 fail2ban 封禁/解封事件（增量 JSONL 审计），合并进安全事件
	banEvents := banCollector.Collect(cfg.Node)
	if len(banEvents) > 0 {
		res.SecurityEvents = append(res.SecurityEvents, banEvents...)
	}

	payload := model.ReportPayload{
		Node:                   cfg.Node,
		Mode:                   modeForModel(cfg),
		IP:                     res.IP,
		OS:                     res.OS,
		Arch:                   res.Arch,
		Group:                  cfg.Group,
		Labels:                 cfg.Labels,
		Version:                version.Version,
		BinSHA256:              agentBinSHA,
		HostInfo:               res.HostInfo,
		Metrics:                res.Metrics,
		Processes:              res.Processes,
		RedisInstances:         res.Redis,
		MySQLInstances:         res.MySQL,
		PostgresInstances:      res.Postgres,
		NginxInstances:         res.Nginx,
		KafkaInstances:         res.Kafka,
		DockerInstances:        res.Docker,
		RocketMQInstances:      res.RocketMQ,
		K8sInstances:           res.K8s,
		MongoDBInstances:       res.MongoDB,
		FastDFSInstances:       res.FastDFS,
		RabbitMQInstances:      res.RabbitMQ,
		ElasticsearchInstances: res.Elasticsearch,
		ClickHouseInstances:    res.ClickHouse,
		NacosInstances:         res.Nacos,
		ZooKeeperInstances:     res.ZooKeeper,
		NginxAccessStats:       res.NginxAccess,
		SecurityEvents:         res.SecurityEvents,
		SecurityBaseline:       res.SecurityBaseline,
		// 声明 Agent 能力：支持结构化入侵防护指令（旧 Server 忽略此字段）
		Capabilities: &model.ClientCapability{
			Defense: true,
			// 声明本机已配置的日志来源（C2）：Server 据此校验上行日志的来源是否属于本节点
			LogSources: logSourceIDs(cfg.LogSources),
			// 声明本机**放行**的下行动作（由 guards.ops 决定，不是"Agent 支持什么"）：
			// Server 只下发声明过的动作，于是"机器不同意"这件事在协议层就生效了。
			Ops: opsSupported(),
		},
		// 上报当前 nebula 托管 SSH 防护状态
		DefenseStatus: defenseExec.Status(),
		// 携带上一次指令执行结果回执（若有）
		DefenseResult:  pendingResult,
		OpsResult:      pendingOpsResult,
		Listeners:      res.Listeners,
		FirewallRules:  res.FirewallRules,
		FirewallStatus: res.FirewallStatus,
		ReportAt:       model.NowMillis(),
	}
	resp, err := rep.ReportFull(payload)
	if err != nil {
		// 错误已在 reporter 内记录，这里仅跳过本轮
		return
	}
	// 回执已随本次上报发出，清空待发结果
	if pendingResult != nil {
		pendingResult = nil
	}
	if pendingOpsResult != nil {
		pendingOpsResult = nil
	}
	slog.Debug("上报成功", "metrics", len(res.Metrics), "procs", len(res.Processes), "banEvents", len(banEvents))

	// 检查 Server 下发的指令
	if resp.Command == "upgrade" {
		slog.Info("收到升级指令，启动自升级流程", "server", cfg.ServerURL)
		upgrader.Run(cfg)
		// 不阻塞：升级脚本先下载并校验新二进制（期间 agent 继续正常运行与上报），
		// 准备就绪后通过 systemctl stop 或升级信号文件停止本进程再替换。
		// 若脚本失败，agent 保持运行，等待 Server 下次心跳重试，不会假死。
		slog.Debug("自升级已在后台执行，agent 继续运行")
	}

	// 处理统一下行操作任务：与防护指令同一模式（先回 running，再异步执行，结果随下轮上报带回）。
	// 异步执行是必须的——诊断包里可能有 ss/systemctl 这类会阻塞几秒的命令，
	// 同步执行会把整轮采集卡住。
	if resp.Ops != nil && opsExec != nil {
		cmd := *resp.Ops
		slog.Info("收到操作任务", "id", cmd.ID, "kind", cmd.Kind, "params", cmd.Params)
		go func() {
			pendingOpsResult = &model.OpsResult{
				CommandID: cmd.ID,
				State:     model.OpsStateRunning,
				At:        model.NowMillis(),
			}
			res := opsExec.Execute(cmd)
			pendingOpsResult = &res
		}()
	}

	// 处理结构化入侵防护指令（enable/disable/status）
	if resp.Defense != nil {
		cmd := *resp.Defense
		slog.Info("收到防护指令", "type", cmd.Type, "id", cmd.ID, "node", cmd.Node)
		go func() {
			// 先回传 running，再异步执行；执行结果暂存至下一次 report 带回
			running := model.DefenseCommandResult{
				CommandID: cmd.ID,
				State:     model.DefenseStateRunning,
				UpdatedAt: model.NowMillis(),
			}
			pendingResult = &running
			res := defenseExec.Execute(cmd)
			pendingResult = &res
		}()
	}
}
