// Package collector 实现主机指标采集。各采集器独立、可开关，结果统一为 model.Metric。
package collector

import (
	"context"
	"time"

	"github.com/shirou/gopsutil/v4/host"

	"github.com/nebula/monitor/internal/agent/config"
	"github.com/nebula/monitor/internal/model"
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
	rabbitmq    *RabbitMQCollector
	es          *ElasticsearchCollector
	clickhouse  *ClickHouseCollector
	nacos       *NacosCollector
	zookeeper   *ZooKeeperCollector
	security    *SecurityCollector

	// logs 是集中日志采集器（C2）。为 nil 表示未配置 logSources，与改造前完全等价。
	logs *LogCollector
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
	rabbitmqInstances []model.RabbitMQInstanceConfig,
	esInstances []model.ElasticsearchInstanceConfig,
	clickhouseInstances []model.ClickHouseInstanceConfig,
	nacosInstances []model.NacosInstanceConfig,
	zookeeperInstances []model.ZooKeeperInstanceConfig,
	portChecks []string,
	securityCfg config.SecurityConfig,
	collectTimeout time.Duration,
	logSources []config.LogSourceConfig,
	logOffsetsPath string,
) *Collector {
	c := &Collector{
		node:    node,
		group:   group,
		labels:  labels,
		cfg:     cfg,
		timeout: collectTimeout,
		cpu:     NewCPUCollector(),
		disk:    NewDiskCollector(),
		net:     NewNetworkCollector(),
	}
	// 集中日志（C2）：未配置来源时不构造采集器，tasks() 也就不追加任务（零行为变化）
	if len(logSources) > 0 {
		c.logs = NewLogCollector(node, logSources, logOffsetsPath)
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
	if cfg.RabbitMQ {
		c.rabbitmq = NewRabbitMQCollector(node, rabbitmqInstances)
	}
	if cfg.Elasticsearch {
		c.es = NewElasticsearchCollector(node, esInstances)
	}
	if cfg.ClickHouse {
		c.clickhouse = NewClickHouseCollector(node, clickhouseInstances)
	}
	if cfg.Nacos {
		c.nacos = NewNacosCollector(node, nacosInstances)
	}
	if cfg.ZooKeeper {
		c.zookeeper = NewZooKeeperCollector(node, zookeeperInstances)
	}
	if cfg.Port {
		c.port = NewPortCollector(node, portChecks)
	}
	if cfg.Security {
		c.security = NewSecurityCollector(node, primaryIP(), securityCfg)
	}
	return c
}

// NodeName 返回采集器使用的节点名（经配置解析后的最终值）。
func (c *Collector) NodeName() string { return c.node }

// SetLogSink 设置日志上行接收方（未配置日志来源时是空操作）。
func (c *Collector) SetLogSink(f model.LogSink) {
	if c.logs != nil {
		c.logs.SetSink(f)
	}
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
