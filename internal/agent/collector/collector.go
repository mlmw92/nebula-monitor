// Package collector 实现主机指标采集。各采集器独立、可开关，结果统一为 model.Metric。
package collector

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/host"

	"github.com/nebula/monitor/internal/agent/config"
	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/template"
)

// Collector 聚合各子采集器，按配置开关产出一批指标。
type Collector struct {
	node   string
	group  string
	labels map[string]string
	cfg    config.CollectorToggle
	// timeout 为单个采集任务的超时（来自 agent.yaml 的 collectTimeout）；0 表示不限制。
	timeout time.Duration

	cpu         *CPUCollector
	disk        *DiskCollector
	net         *NetworkCollector
	redis       *RedisCollector
	mysql       *MySQLCollector
	pg          *PostgresCollector
	nginx       *NginxCollector
	nginxAccess *NginxAccessCollector
	kafka       *KafkaCollector
	docker      *DockerCollector
	rmq         *RocketMQCollector
	k8s         *K8sCollector
	port        *PortCollector
	mongo       *MongoDBCollector
	fastdfs     *FastDFSCollector
	security    *SecurityCollector

	// 采集项模板（阶段一：来自本机 agent.yaml；阶段二：可被 Server 下发替换）。
	// 加锁的原因：tasks() 每轮都会读取该集合，而下发路径可在任意时刻原子替换它。
	tplMu          sync.RWMutex
	templates      []template.Config
	tplRevision    uint64 // 已生效的模板版本号（0 = 仅本机配置，从未接受过下发）
	templateRunner *TemplateRunner

	// guards 是模板「从本机 / 数据库取数」的本机护栏（阶段三）。
	// 零值 = 三类全部未放行，因此「忘记注入」的后果是「本机取数不可用」，
	// 而不是「悄悄允许了 root 执行」——默认值必须站在安全的一侧。
	guards config.TemplateGuardsConfig
}

// New 创建 Collector。
func New(node, group string, labels map[string]string, cfg config.CollectorToggle,
	redisInstances []model.RedisInstanceConfig,
	mysqlInstances []model.MySQLInstanceConfig,
	postgresInstances []model.PostgresInstanceConfig,
	nginxInstances []model.NginxInstanceConfig,
	kafkaInstances []model.KafkaInstanceConfig,
	dockerInstances []model.DockerInstanceConfig,
	rocketmqInstances []model.RocketMQInstanceConfig,
	k8sInstances []model.K8sInstanceConfig,
	mongoInstances []model.MongoDBInstanceConfig,
	fastdfsInstances []model.FastDFSInstanceConfig,
	portChecks []string,
	securityCfg config.SecurityConfig,
	collectTimeout time.Duration,
	templates []template.Config,
	guards config.TemplateGuardsConfig,
) *Collector {
	c := &Collector{
		node:    node,
		group:   group,
		labels:  labels,
		cfg:     cfg,
		timeout: collectTimeout,
		guards:  guards,
		cpu:     NewCPUCollector(),
		disk:    NewDiskCollector(),
		net:     NewNetworkCollector(),
	}
	if cfg.Redis {
		c.redis = NewRedisCollector(node, redisInstances)
	}
	if cfg.MySQL {
		c.mysql = NewMySQLCollector(node, mysqlInstances)
	}
	if cfg.Postgres {
		c.pg = NewPostgresCollector(node, postgresInstances)
	}
	if cfg.Nginx {
		c.nginx = NewNginxCollector(node, nginxInstances)
	}
	if cfg.NginxLog {
		c.nginxAccess = NewNginxAccessCollector(node, group, nginxInstances)
	}
	if cfg.Kafka {
		c.kafka = NewKafkaCollector(node, kafkaInstances)
	}
	if cfg.Docker {
		c.docker = NewDockerCollector(node, dockerInstances)
	}
	if cfg.RocketMQ {
		c.rmq = NewRocketMQCollector(node, rocketmqInstances)
	}
	if cfg.K8s {
		c.k8s = NewK8sCollector(node, k8sInstances)
	}
	if cfg.MongoDB {
		c.mongo = NewMongoDBCollector(node, mongoInstances)
	}
	if cfg.FastDFS {
		c.fastdfs = NewFastDFSCollector(node, fastdfsInstances)
	}
	if cfg.Port {
		c.port = NewPortCollector(node, portChecks)
	}
	if cfg.Security {
		c.security = NewSecurityCollector(node, primaryIP(), securityCfg)
	}
	// 模板不设独立开关：配置了模板即启用；为空则 tasks() 不追加任务（零行为变化）。
	if len(templates) > 0 {
		c.templates = templates
		c.templateRunner = NewTemplateRunner(node).WithGuards(guards)
	}
	return c
}

// SetTemplates 原子替换模板集（Server 下发路径使用）。
//
// 为什么不需要重启、也不需要 SIGHUP：CollectAll 每轮都重建任务表（tasks()），
// 这里换掉集合，下一个采集周期自然生效。
// 版本号 0 表示「仅本机 agent.yaml 配置」，因此首次下发（revision ≥ 1）必然与 0 不等而生效。
func (c *Collector) SetTemplates(tpls []template.Config, revision uint64) {
	c.tplMu.Lock()
	defer c.tplMu.Unlock()
	if c.templateRunner == nil {
		c.templateRunner = NewTemplateRunner(c.node).WithGuards(c.guards)
	}
	c.templates = tpls
	c.tplRevision = revision
}

// Templates 返回当前生效的模板集（副本）。
func (c *Collector) Templates() []template.Config {
	c.tplMu.RLock()
	defer c.tplMu.RUnlock()
	return append([]template.Config(nil), c.templates...)
}

// TemplateRevision 返回当前已生效的模板版本号（0 表示从未接受过下发）。
func (c *Collector) TemplateRevision() uint64 {
	c.tplMu.RLock()
	defer c.tplMu.RUnlock()
	return c.tplRevision
}

// ApplyDelivered 应用 Server 下发的模板集：先整体校验，通过后才原子替换。
//
// 校验不通过时**保留现有模板**并返回错误——模板下发属运维便利功能，
// 绝不能因为它把采集打断（宁可继续用旧配置，也不能进入「没有模板」的状态）。
func (c *Collector) ApplyDelivered(tpls []template.Config, revision uint64) error {
	// 纵深防御：Server 侧已按节点能力过滤，但「机器自身的同意」不能只依赖中心。
	// 丢弃而非拒绝整份下发——一台机器不该因为别人的 exec 模板而丢掉自己所有的模板。
	kept, skipped := c.dropUnpermittedKinds(tpls)
	if len(skipped) > 0 {
		slog.Warn("Server 下发的模板含本机未放行的取数方式，已忽略",
			"kinds", skipped, "hint", "如需使用，请在 agent.yaml 的 templateGuards 中启用并加入白名单")
	}
	if err := template.ValidateAll(kept); err != nil {
		return err
	}
	// 首次被 Server 接管时明确告知：本机 agent.yaml 里的模板将被替换，
	// 否则运维会困惑于「本地写的模板怎么不见了」。
	if c.TemplateRevision() == 0 && len(c.Templates()) > 0 {
		slog.Warn("Server 下发的模板将替换本机 agent.yaml 中的模板（之后以 Server 配置为准）",
			"local", len(c.Templates()), "delivered", len(kept), "revision", revision)
	}
	c.SetTemplates(kept, revision)
	return nil
}

// dropUnpermittedKinds 丢弃本机未放行的护栏类模板，返回保留的模板与被丢弃的取数方式（去重）。
func (c *Collector) dropUnpermittedKinds(tpls []template.Config) ([]template.Config, []string) {
	kept := make([]template.Config, 0, len(tpls))
	var skipped []string
	seen := make(map[string]bool, len(tpls))
	for _, t := range tpls {
		if template.IsGuardedKind(t.Kind) && !c.guards.AllowsKind(t.Kind) {
			if !seen[string(t.Kind)] {
				seen[string(t.Kind)] = true
				skipped = append(skipped, string(t.Kind))
			}
			continue
		}
		kept = append(kept, t)
	}
	return kept, skipped
}

// templateState 一次性取出「模板集 + 执行器」，避免分别加锁读到不一致的组合。
func (c *Collector) templateState() ([]template.Config, *TemplateRunner) {
	c.tplMu.RLock()
	defer c.tplMu.RUnlock()
	if len(c.templates) == 0 || c.templateRunner == nil {
		return nil, nil
	}
	return append([]template.Config(nil), c.templates...), c.templateRunner
}

// Collect 采集所有启用指标（等价于 CollectCtx(context.Background())）。
func (c *Collector) Collect() ([]model.Metric, []model.ProcessStat) {
	return c.CollectCtx(context.Background())
}

// CollectCtx 采集所有启用指标，并填充节点基础信息。
// 返回的 metrics 已带 node 标签；group 由 Server 在写入时补充。
//
// 主机类采集（CPU / 内存 / 负载 / 磁盘 / 网络 / 进程）走 gopsutil 本机系统调用，
// 无 ctx 接口、耗时本身有上界，因此这里做「入口门控」：ctx 已结束时跳过该阶段，
// 避免任务超时后仍继续做无意义的本机扫描；端口探测属网络 I/O，完整受 ctx 约束。
func (c *Collector) CollectCtx(ctx context.Context) ([]model.Metric, []model.ProcessStat) {
	now := model.NowMillis()
	var metrics []model.Metric
	add := func(name string, value float64, labels map[string]string) {
		m := model.Metric{Node: c.node, Name: name, Value: value, Timestamp: now}
		if labels != nil {
			m.Labels = labels
		}
		metrics = append(metrics, m)
	}
	aborted := func() bool { return ctx.Err() != nil }

	if c.cfg.CPU && !aborted() {
		for _, m := range c.cpu.CollectCtx(ctx) {
			add(m.Name, m.Value, m.Labels)
		}
	}
	if c.cfg.Memory && !aborted() {
		for _, m := range collectMemory() {
			add(m.Name, m.Value, m.Labels)
		}
	}
	if c.cfg.Load && !aborted() {
		for _, m := range collectLoad() {
			add(m.Name, m.Value, m.Labels)
		}
	}
	if c.cfg.Disk && !aborted() {
		for _, m := range c.disk.CollectCtx(ctx) {
			add(m.Name, m.Value, m.Labels)
		}
	}
	if c.cfg.Network && !aborted() {
		for _, m := range c.net.CollectCtx(ctx) {
			add(m.Name, m.Value, m.Labels)
		}
	}
	if c.cfg.Port && c.port != nil {
		for _, m := range c.port.CollectCtx(ctx) {
			add(m.Name, m.Value, m.Labels)
		}
	}

	var procs []model.ProcessStat
	if c.cfg.Process && !aborted() {
		var total int
		procs, total = collectProcessTop()
		// 进程总数单独上报为时序指标，供大屏「进程总数」等聚合展示使用
		add("process_total", float64(total), nil)
	}
	return metrics, procs
}

// CollectSecurity 采集安全事件与基线检查结果（等价于 CollectSecurityCtx(context.Background())）。
// 返回该节点的安全事件列表与基线评分（可为 nil，表示未启用安全采集）。
func (c *Collector) CollectSecurity() ([]model.SecurityEvent, *model.SecurityBaseline) {
	return c.CollectSecurityCtx(context.Background())
}

// CollectSecurityCtx 采集安全事件与基线检查结果；ctx 结束时跳过后续基线检查。
func (c *Collector) CollectSecurityCtx(ctx context.Context) ([]model.SecurityEvent, *model.SecurityBaseline) {
	if c.security == nil {
		return nil, nil
	}
	return c.security.CollectCtx(ctx)
}

// CollectListeners 采集监听端口列表（等价于 CollectListenersCtx(context.Background())）。
func (c *Collector) CollectListeners() []model.ListenerStat {
	return c.CollectListenersCtx(context.Background())
}

// CollectListenersCtx 采集监听端口列表（TCP/UDP），用于端口监控 Tab。
// 该采集为只读本地端口快照（类似 ss -tlnp），开销极小，
// 不依赖 port 存活探测开关（cfg.Port），默认即开启。
func (c *Collector) CollectListenersCtx(ctx context.Context) []model.ListenerStat {
	if ctx.Err() != nil {
		return nil
	}
	return collectListeners()
}

// CollectFirewallRules 采集防火墙规则列表（等价于 CollectFirewallRulesCtx(context.Background())）。
func (c *Collector) CollectFirewallRules() []model.FirewallRule {
	return c.CollectFirewallRulesCtx(context.Background())
}

// CollectFirewallRulesCtx 采集防火墙规则列表，用于防火墙监控 Tab。
// 该采集为只读本地规则快照（iptables/nftables/ufw），开销极小，
// 不依赖 security 安全扫描开关（cfg.Security），默认即开启。
func (c *Collector) CollectFirewallRulesCtx(ctx context.Context) []model.FirewallRule {
	return collectFirewallRules(ctx)
}

// CollectFirewallStatus 采集防火墙整体状态（等价于 CollectFirewallStatusCtx(context.Background())）。
func (c *Collector) CollectFirewallStatus(ruleCount int) *model.FirewallStatus {
	return c.CollectFirewallStatusCtx(context.Background(), ruleCount)
}

// CollectFirewallStatusCtx 采集防火墙整体状态（后端类型/运行/自启/版本等），用于防火墙监控 Tab 顶部状态展示。
// ruleCount 为本次已采集到的防火墙规则条数。
func (c *Collector) CollectFirewallStatusCtx(ctx context.Context, ruleCount int) *model.FirewallStatus {
	return collectFirewallStatus(ctx, ruleCount)
}

// HostInfo 返回主机静态信息（OS/Arch/IP）（等价于 HostInfoCtx(context.Background())）。
func (c *Collector) HostInfo() (os, arch, ip string) {
	return c.HostInfoCtx(context.Background())
}

// HostInfoCtx 返回主机静态信息（OS/Arch/IP），用于上报体。
// host.Info() 为 gopsutil 本机调用，无 ctx 接口，仅在入口做门控。
func (c *Collector) HostInfoCtx(ctx context.Context) (os, arch, ip string) {
	if ctx.Err() != nil {
		return "", "", ""
	}
	info, err := host.Info()
	if err == nil {
		os = info.OS + " " + info.Platform + " " + info.PlatformVersion
	}
	arch = hostArch()
	ip = primaryIP()
	return
}
