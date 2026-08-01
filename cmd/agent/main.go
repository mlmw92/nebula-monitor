// Command agent 是被监控节点上运行的轻量级采集上报进程。
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
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
