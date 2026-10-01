// Package alert 实现阈值告警引擎：规则管理、评估、通知。
package alert

import (
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/config"
)

// RulesStore 管理告警规则，持久化到 YAML 文件。
type RulesStore struct {
	mu    sync.RWMutex
	rules map[string]model.AlertRule
	path  string
}

// NewRulesStore 创建规则存储并加载。若规则文件不存在（全新安装），
// 自动播种一组开箱即用的推荐规则，避免告警中心初始为空、看起来“没用起来”。
func NewRulesStore(path string) *RulesStore {
	s := &RulesStore{
		rules: map[string]model.AlertRule{},
		path:  path,
	}
	s.load()
	s.SeedDefaults()
	return s
}

// DefaultTemplates 返回可复用的规则模板（不含 ID/时间戳），供前端「从模板新建」挑选。
//
// 这里是「可以配什么」的**目录**（65 条，按 TemplateGroup 分组）：它不等于自动创建，
// 自动创建的子集见 seedTemplates——两者必须分开，否则一次升级会在用户规则列表里
// 塞进几十条"这台机器上根本不存在的对象"的规则，把真规则淹掉。
//
// 每条模板都要写清三件事：**指标名**（必须是采集器真正产出的名字，见 internal/server/metrics）、
// **变差方向**（越大越糟还是越小越糟，即 Operator 的方向）与 **Desc**（阈值依据与调整建议）。
// 「怎么配」之所以难，就难在这三件事——尤其是方向，方向配反的症状是规则永不触发。
func DefaultTemplates() []model.AlertRule {
	return []model.AlertRule{
		// ===== 主机 =====
		{Name: "CPU 使用率过高", TemplateGroup: "主机", Metric: "cpu_usage", Operator: ">", Threshold: 85, For: "5m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true,
			Desc: "持续 5 分钟 > 85%。偶发尖峰是正常的，用 For 过滤掉；长期偏高优先看是哪个进程"},
		{Name: "内存使用率过高", TemplateGroup: "主机", Metric: "mem_used_percent", Operator: ">", Threshold: 90, For: "5m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true,
			Desc: "> 90% 且持续 5 分钟。注意 Linux 会把缓存计入已用，判断真实余量看 mem_available_bytes"},
		{Name: "磁盘使用率过高", TemplateGroup: "主机", Metric: "disk_used_percent", Operator: ">", Threshold: 85, For: "5m",
			Severity: model.SeverityCritical, Scope: "all", Enabled: true,
			Desc: "> 85% 即 critical：磁盘写满会让日志、数据库同时不可用。该指标已做真实磁盘汇总，不会因某个小分区误报"},
		{Name: "系统负载过高", TemplateGroup: "主机", Metric: "load1", Operator: ">", Threshold: 8, For: "5m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true,
			Desc: "阈值应按 CPU 核数调整，建议设为「核数 × 2」（8 核机器用 16）"},
		{Name: "Swap 使用率过高", TemplateGroup: "主机", Metric: "swap_used_percent", Operator: ">", Threshold: 50, For: "10m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true,
			Desc: "Swap 被大量占用通常意味着物理内存不足，会连带把数据库延迟拖高"},
		{Name: "可用内存不足", TemplateGroup: "主机", Metric: "mem_available_bytes", Operator: "<", Threshold: 536870912, For: "5m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true,
			Desc: "可用内存（含可回收缓存）低于 512MiB。注意方向是「越小越糟」，故用小于号"},
		{Name: "TCP 重传速率过高", TemplateGroup: "主机", Metric: "tcp_retransmit_rate", Operator: ">", Threshold: 100, For: "5m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true,
			Desc: "持续重传说明链路质量或网卡有问题；内网正常水位通常接近 0"},
		{Name: "主机离线", TemplateGroup: "主机", Type: model.RuleTypeNodeOffline, For: "5m", Severity: model.SeverityCritical, Scope: "all", Enabled: true,
			Escalation: &model.Escalation{Enabled: true, AfterMinutes: 15, ToSeverity: model.SeverityCritical, RepeatMinutes: 30},
			Desc:       "Agent 心跳超时；这是「这台机器整个不见了」的总开关"},

		// ===== MySQL =====
		{Name: "MySQL 服务离线", TemplateGroup: "MySQL", Type: model.RuleTypeServiceDown, Service: "mysql", Operator: "<=", Threshold: 0, For: "3m",
			Severity: model.SeverityCritical, Scope: "all", Enabled: true, Desc: "mysql_instance_up 归零"},
		{Name: "MySQL 主从切换", TemplateGroup: "MySQL", Type: model.RuleTypeRoleChange, Service: "mysql", Topology: "cluster", For: "0s",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true,
			Escalation: &model.Escalation{Enabled: true, AfterMinutes: 10, ToSeverity: model.SeverityCritical, RepeatMinutes: 0},
			Desc:       "角色标签发生变化：可能是故障切换，也可能是有人手工操作"},
		{Name: "MySQL 集群状态损坏", TemplateGroup: "MySQL", Type: model.RuleTypeClusterFault, Service: "mysql", Topology: "cluster", For: "2m",
			Severity: model.SeverityCritical, Scope: "all", Enabled: true,
			Escalation: &model.Escalation{Enabled: true, AfterMinutes: 10, ToSeverity: model.SeverityCritical, RepeatMinutes: 20},
			Desc:       "Group Replication 无主或成员视图不一致（脑裂）"},
		{Name: "MySQL 主从延迟过大", TemplateGroup: "MySQL", Metric: "mysql_seconds_behind_master", Operator: ">", Threshold: 30, For: "5m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true,
			Desc: "从库落后主库 30 秒以上。读流量打到从库时会造成「读到旧数据」，比停机更难排查"},
		{Name: "MySQL 连接数接近上限", TemplateGroup: "MySQL", Metric: "mysql_threads_connected", Operator: ">", Threshold: 400, For: "5m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true,
			Desc: "建议设为 mysql_max_connections 的 80%（默认上限 500 时约 400）；打满会出现客户端连接被拒"},
		{Name: "MySQL 缓冲池命中率过低", TemplateGroup: "MySQL", Metric: "mysql_innodb_buffer_pool_hit_rate", Operator: "<", Threshold: 95, For: "10m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true,
			Desc: "低于 95% 说明缓冲池装不下热数据，磁盘读会明显增多；优先排查 pg 大小与慢 SQL"},
		{Name: "MySQL 查询延迟过高", TemplateGroup: "MySQL", Metric: "mysql_query_latency_ms", Operator: ">", Threshold: 200, For: "5m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true, Desc: "按模板调整：OLTP 业务通常 50~200ms 就该关注"},

		// ===== PostgreSQL =====
		{Name: "PostgreSQL 服务离线", TemplateGroup: "PostgreSQL", Type: model.RuleTypeServiceDown, Service: "postgres", Operator: "<=", Threshold: 0, For: "3m",
			Severity: model.SeverityCritical, Scope: "all", Enabled: true, Desc: "postgres_instance_up 归零"},
		{Name: "PostgreSQL 主从切换", TemplateGroup: "PostgreSQL", Type: model.RuleTypeRoleChange, Service: "postgres", Topology: "replication", For: "0s",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true,
			Desc:       "角色标签发生变化：可能是故障切换（此时应立即确认新主库可用），也可能是人工操作"},
		{Name: "PostgreSQL 复制延迟过大", TemplateGroup: "PostgreSQL", Metric: "postgres_replication_lag_bytes", Operator: ">", Threshold: 67108864, For: "5m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true, Desc: "落后超过 64MiB（按 WAL 字节数衡量）"},
		{Name: "PostgreSQL 缓存命中率过低", TemplateGroup: "PostgreSQL", Metric: "postgres_cache_hit_ratio", Operator: "<", Threshold: 95, For: "10m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true, Desc: "低于 95% 说明 shared_buffers 偏小或存在大量全表扫描"},

		// ===== Redis =====
		{Name: "Redis 服务离线", TemplateGroup: "Redis", Type: model.RuleTypeServiceDown, Service: "redis", Operator: "<=", Threshold: 0, For: "3m",
			Severity: model.SeverityCritical, Scope: "all", Enabled: true, Desc: "redis_instance_up 归零"},
		{Name: "Redis 内存使用率过高", TemplateGroup: "Redis", Metric: "redis_used_memory_percent", Operator: ">", Threshold: 85, For: "5m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true,
			Desc: "相对 maxmemory 的占用；未设置 maxmemory 时该指标不可用，用 redis_used_memory 自行设绝对值"},
		{Name: "Redis 命中率过低", TemplateGroup: "Redis", Metric: "redis_hit_rate", Operator: "<", Threshold: 80, For: "10m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true, Desc: "命中率骤降往往是键过期策略或缓存穿透引起的"},
		{Name: "Redis 内存碎片率过高", TemplateGroup: "Redis", Metric: "redis_memory_fragmentation_ratio", Operator: ">", Threshold: 1.5, For: "15m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true, Desc: "持续 > 1.5 说明碎片严重，可开启 activedefrag 或安排重启"},
		{Name: "Redis 连接数接近上限", TemplateGroup: "Redis", Metric: "redis_connected_clients", Operator: ">", Threshold: 800, For: "5m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true,
			Desc: "建议设为 redis_maxclients 的 80%（默认 10000 时约 8000）；触顶会出现连接被拒"},
		{Name: "Redis 主从延迟过大", TemplateGroup: "Redis", Metric: "redis_replication_lag", Operator: ">", Threshold: 10, For: "5m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true,
			Desc:       "从库落后 10 秒以上；过大会让故障切换时丢数据，也会读到旧值"},
		{Name: "Redis 集群槽位异常", TemplateGroup: "Redis", Metric: "redis_cluster_slots_fail", Operator: ">", Threshold: 0, For: "5m",
			Severity: model.SeverityCritical, Scope: "all", Enabled: true, Desc: "存在故障槽位意味着这部分键彻底不可用"},

		// ===== Nginx =====
		{Name: "Nginx 服务离线", TemplateGroup: "Nginx", Type: model.RuleTypeServiceDown, Service: "nginx", Operator: "<=", Threshold: 0, For: "3m",
			Severity: model.SeverityCritical, Scope: "all", Enabled: true, Desc: "nginx_instance_up 归零"},
		{Name: "Nginx 连接丢弃速率过高", TemplateGroup: "Nginx", Metric: "nginx_connection_drop_rate", Operator: ">", Threshold: 1, For: "5m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true,
			Desc: "accepts 与 handled 的差值持续增长，说明 backlog 打满、有连接被丢"},
		{Name: "Nginx 活跃连接数过高", TemplateGroup: "Nginx", Metric: "nginx_active_connections", Operator: ">", Threshold: 5000, For: "5m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true, Desc: "按 worker_connections 总量调整"},

		// ===== Kafka =====
		{Name: "Kafka 服务离线", TemplateGroup: "Kafka", Type: model.RuleTypeServiceDown, Service: "kafka", Operator: "<=", Threshold: 0, For: "3m",
			Severity: model.SeverityCritical, Scope: "all", Enabled: true,
			Desc:       "kafka_instance_up 归零；按 Broker 实例维度判定"},
		{Name: "Kafka 副本不足分区", TemplateGroup: "Kafka", Metric: "kafka_under_replicated_partitions", Operator: ">", Threshold: 0, For: "5m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true, Desc: "持续不为 0：有 Broker 掉线或磁盘故障，副本机制暂时失效"},
		{Name: "Kafka 离线分区", TemplateGroup: "Kafka", Metric: "kafka_offline_partitions", Operator: ">", Threshold: 0, For: "2m",
			Severity: model.SeverityCritical, Scope: "all", Enabled: true, Desc: "任何非 0 都意味着这部分分区不可读写"},
		{Name: "Kafka 消费积压过大", TemplateGroup: "Kafka", Metric: "kafka_consumer_lag_max", Operator: ">", Threshold: 100000, For: "10m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true,
			Desc: "取所有消费组里的最大积压，最贴近「业务是否在堆积」；阈值按业务吞吐调整"},
		{Name: "Kafka Controller 异常", TemplateGroup: "Kafka", Metric: "kafka_active_controller_count", Operator: "!=", Threshold: 1, For: "2m",
			Severity: model.SeverityCritical, Scope: "all", Enabled: true, Desc: "正常恒为 1；大于 1 是脑裂，为 0 表示无主，都会导致元数据操作失败"},

		// ===== Elasticsearch =====
		{Name: "Elasticsearch 集群非 green", TemplateGroup: "Elasticsearch", Metric: "es_cluster_status", Operator: ">=", Threshold: 1, For: "5m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true, Desc: "yellow：副本分片未分配，数据还在但没有冗余"},
		{Name: "Elasticsearch 集群 red", TemplateGroup: "Elasticsearch", Metric: "es_cluster_status", Operator: ">=", Threshold: 2, For: "2m",
			Severity: model.SeverityCritical, Scope: "all", Enabled: true, Desc: "red：主分片缺失，部分数据不可读写"},
		{Name: "Elasticsearch 未分配分片", TemplateGroup: "Elasticsearch", Metric: "es_unassigned_shards", Operator: ">", Threshold: 0, For: "10m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true,
			Desc: "yellow/red 的直接原因；持续不为 0 优先看磁盘水位与分片分配策略"},

		// ===== ClickHouse =====
		{Name: "ClickHouse 服务离线", TemplateGroup: "ClickHouse", Type: model.RuleTypeServiceDown, Service: "clickhouse", Operator: "<=", Threshold: 0, For: "3m",
			Severity: model.SeverityCritical, Scope: "all", Enabled: true,
			Desc:       "clickhouse_instance_up 归零"},
		{Name: "ClickHouse 合并积压", TemplateGroup: "ClickHouse", Metric: "clickhouse_merges_running", Operator: ">", Threshold: 20, For: "10m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true, Desc: "合并长期排队说明写入过快或分区粒度过细"},

		// ===== Nacos / ZooKeeper / RabbitMQ =====
		{Name: "Nacos 服务离线", TemplateGroup: "Nacos", Type: model.RuleTypeServiceDown, Service: "nacos", Operator: "<=", Threshold: 0, For: "3m",
			Severity: model.SeverityCritical, Scope: "all", Enabled: true, Desc: "注册中心不可用会让服务发现整体失效"},
		{Name: "ZooKeeper 服务离线", TemplateGroup: "ZooKeeper", Type: model.RuleTypeServiceDown, Service: "zookeeper", Operator: "<=", Threshold: 0, For: "3m",
			Severity: model.SeverityCritical, Scope: "all", Enabled: true,
			Desc:       "zookeeper_instance_up 归零；ZK 掉节点会影响依赖它的注册中心与协调服务"},
		{Name: "ZooKeeper 请求排队", TemplateGroup: "ZooKeeper", Metric: "zookeeper_outstanding_requests", Operator: ">", Threshold: 10, For: "5m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true, Desc: "持续排队说明请求处理跟不上，常见于磁盘慢或快照/事务日志同盘"},
		{Name: "RabbitMQ 服务离线", TemplateGroup: "RabbitMQ", Type: model.RuleTypeServiceDown, Service: "rabbitmq", Operator: "<=", Threshold: 0, For: "3m",
			Severity: model.SeverityCritical, Scope: "all", Enabled: true,
			Desc:       "rabbitmq_instance_up 归零"},
		{Name: "RabbitMQ 队列积压", TemplateGroup: "RabbitMQ", Metric: "rabbitmq_queue_messages", Operator: ">", Threshold: 10000, For: "10m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true, Desc: "积压持续增长说明消费者跟不上生产"},
		{Name: "RabbitMQ 文件描述符接近上限", TemplateGroup: "RabbitMQ", Metric: "rabbitmq_fd_used", Operator: ">", Threshold: 50000, For: "10m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true, Desc: "接近 ulimit 时会出现连接被拒"},

		// ===== RocketMQ / MongoDB / FastDFS / Docker =====
		{Name: "RocketMQ 服务离线", TemplateGroup: "RocketMQ", Type: model.RuleTypeServiceDown, Service: "rocketmq", Operator: "<=", Threshold: 0, For: "3m",
			Severity: model.SeverityCritical, Scope: "all", Enabled: true,
			Desc:       "rocketmq_instance_up 归零"},
		{Name: "RocketMQ 消息积压", TemplateGroup: "RocketMQ", Metric: "rocketmq_message_accumulation", Operator: ">", Threshold: 10000, For: "10m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true,
			Desc:       "积压超过 1 万条且持续 10 分钟；按业务吞吐与可接受延迟调整"},
		{Name: "MongoDB 服务离线", TemplateGroup: "MongoDB", Type: model.RuleTypeServiceDown, Service: "mongodb", Operator: "<=", Threshold: 0, For: "3m",
			Severity: model.SeverityCritical, Scope: "all", Enabled: true,
			Desc:       "mongodb_up 归零"},
		{Name: "MongoDB 副本延迟过大", TemplateGroup: "MongoDB", Metric: "mongodb_repl_lag", Operator: ">", Threshold: 10, For: "5m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true,
			Desc:       "从节点落后 10 秒以上；过大会让「读从库」拿到旧数据"},
		{Name: "FastDFS 服务离线", TemplateGroup: "FastDFS", Type: model.RuleTypeServiceDown, Service: "fastdfs", Operator: "<=", Threshold: 0, For: "3m",
			Severity: model.SeverityCritical, Scope: "all", Enabled: true,
			Desc:       "fastdfs_up 归零（Tracker/Storage 整体不可用）"},
		{Name: "FastDFS Storage 掉线", TemplateGroup: "FastDFS", Metric: "fastdfs_storage_offline_count", Operator: ">", Threshold: 0, For: "5m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true, Desc: "有 Storage 节点离线，存储容量与冗余随之下降"},
		{Name: "Docker 容器离线", TemplateGroup: "Docker", Type: model.RuleTypeServiceDown, Service: "docker", Operator: "<=", Threshold: 0, For: "3m",
			Severity: model.SeverityCritical, Scope: "all", Enabled: true, Desc: "注意：容器退出不会从实例清单消失，该规则按容器存活指标判定"},

		// ===== Kubernetes =====
		{Name: "Kubernetes 集群离线", TemplateGroup: "Kubernetes", Type: model.RuleTypeServiceDown, Service: "k8s", Operator: "<=", Threshold: 0, For: "3m",
			Severity: model.SeverityCritical, Scope: "all", Enabled: true,
			Desc:       "k8s_cluster_up 归零：采集侧连不上 apiserver"},
		{Name: "Kubernetes 集群状态损坏", TemplateGroup: "Kubernetes", Type: model.RuleTypeClusterFault, Service: "k8s", Topology: "cluster", For: "2m",
			Severity: model.SeverityCritical, Scope: "all", Enabled: true,
			Desc:       "就绪节点数低于期望或出现分裂状态"},
		{Name: "Kubernetes 异常 Deployment", TemplateGroup: "Kubernetes", Metric: "k8s_deployments_unhealthy", Operator: ">", Threshold: 0, For: "10m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true, Desc: "就绪副本数未达期望，通常是镜像拉取或探针失败"},
		{Name: "Kubernetes Pending Pod 过多", TemplateGroup: "Kubernetes", Metric: "k8s_pods_pending", Operator: ">", Threshold: 10, For: "10m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true, Desc: "长期 Pending 通常是资源不足或调度约束无法满足"},
		{Name: "Kubernetes 节点未就绪", TemplateGroup: "Kubernetes", Metric: "k8s_node_ready", Operator: "<", Threshold: 1, For: "5m",
			Severity: model.SeverityCritical, Scope: "all", Enabled: true, Desc: "带节点维度；任一节点 NotReady 都会触发"},

		// ===== 拨测与证书 =====
		{Name: "拨测失败", TemplateGroup: "拨测与证书", Metric: "dial_test_up", Operator: "<=", Threshold: 0, For: "3m",
			Severity: model.SeverityCritical, Scope: "all", Enabled: true,
			Desc: "任务按拨测任务维度上报；失败即业务入口不可达"},
		{Name: "拨测延迟过高", TemplateGroup: "拨测与证书", Metric: "dial_test_latency", Operator: ">", Threshold: 2000, For: "5m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true, Desc: "端到端耗时（含 DNS 与 TLS 握手），按业务 SLO 调整"},
		{Name: "证书即将到期", TemplateGroup: "拨测与证书", Metric: "dial_test_cert_expiry", Operator: "<", Threshold: 15, For: "1h",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true,
			Desc: "剩余天数小于 15 天。方向是越小越糟，务必用小于号——设成大于号会让规则永不触发（静默失效）"},
		{Name: "证书已过期", TemplateGroup: "拨测与证书", Metric: "dial_test_cert_expiry", Operator: "<", Threshold: 1, For: "5m",
			Severity: model.SeverityCritical, Scope: "all", Enabled: true, Desc: "剩余不足 1 天：浏览器已开始拦截"},
		{Name: "端口不可达", TemplateGroup: "拨测与证书", Metric: "port_up", Operator: "<=", Threshold: 0, For: "5m",
			Severity: model.SeverityCritical, Scope: "all", Enabled: true, Desc: "Agent 侧 TCP 探测失败；按被探测端口维度上报"},

		// ===== 集中日志 =====
		{Name: "日志采集不可用", TemplateGroup: "集中日志", Metric: "log_up", Operator: "<=", Threshold: 0, For: "10m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true,
			Desc: "该来源本轮所有路径都读不到（文件被轮转/权限变化/路径写错）——「配了却没有数据」的第一现场信号"},
		{Name: "日志采集丢弃", TemplateGroup: "集中日志", Metric: "log_dropped_total", Operator: ">", Threshold: 0, For: "10m",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true, Desc: "触发单轮上限或解析异常时会计数；丢弃是正常结果，但不该持续发生"},

		// ===== 安全 =====
		{Name: "安全事件", TemplateGroup: "安全", Type: model.RuleTypeSecurityEvent, Category: "", For: "0s",
			Severity: model.SeverityWarning, Scope: "all", Enabled: true,
			Desc: "覆盖 root 登录、空口令、防火墙关闭、fail2ban 未运行等基线项与暴力破解等事件"},
	}
}

// seededTemplateNames 是「全新安装 / 版本升级时自动创建」的模板名集合。
//
// 为什么只播种一小部分：模板库（DefaultTemplates）是**可以配什么**的目录，65 条；
// 而自动创建会直接进入用户的规则列表。若全量播种，一次升级就会塞进几十条
// "这台机器上根本不存在的对象"的规则（例如没装 ClickHouse 也会有一条 ClickHouse 规则），
// 结果是真规则被淹、用户开始忽略告警列表。
//
// 入选标准：**跨环境普遍成立、且不需要用户先改阈值就能用**。
// 需要按机器规模调整阈值的（负载、连接数、积压量）一律不进播种，只作为模板供挑选。
var seededTemplateNames = []string{
	"CPU 使用率过高", "内存使用率过高", "磁盘使用率过高", "系统负载过高",
	"主机离线",
	"MySQL 服务离线", "Redis 服务离线", "Nginx 服务离线", "Kafka 服务离线", "Kubernetes 集群离线",
	"PostgreSQL 服务离线",
	"MySQL 主从切换", "PostgreSQL 主从切换",
	"MySQL 集群状态损坏", "Kubernetes 集群状态损坏",
	"Elasticsearch 集群非 green", "Kafka 消费积压过大",
	"拨测失败", "证书即将到期",
	"安全事件",
}

// seedTemplates 返回自动创建用的内置规则（DefaultTemplates 的子集，按 seededTemplateNames 过滤）。
func seedTemplates() []model.AlertRule {
	want := make(map[string]bool, len(seededTemplateNames))
	for _, n := range seededTemplateNames {
		want[n] = true
	}
	out := make([]model.AlertRule, 0, len(seededTemplateNames))
	for _, t := range DefaultTemplates() {
		if want[t.Name] {
			out = append(out, t)
		}
	}
	return out
}

// seedMarkerPath 返回播种记录文件路径，记录已经播种过的内置规则名。
func (s *RulesStore) seedMarkerPath() string { return s.path + ".seeded" }

// loadSeedMarker 读取已播种的内置规则名集合。文件不存在时返回空集合。
func (s *RulesStore) loadSeedMarker() map[string]bool {
	seeded := map[string]bool{}
	data, err := os.ReadFile(s.seedMarkerPath())
	if err != nil {
		return seeded
	}
	var names []string
	if err := yaml.Unmarshal(data, &names); err != nil {
		slog.Warn("解析告警规则播种记录失败", "path", s.seedMarkerPath(), "err", err)
		return seeded
	}
	for _, n := range names {
		seeded[n] = true
	}
	return seeded
}

// saveSeedMarker 记录当前版本全部内置规则名，表示它们均已播种过。
// 之后用户删除其中任何一条都不会被重新写回。
func (s *RulesStore) saveSeedMarker(names []string) {
	data, err := yaml.Marshal(names)
	if err != nil {
		return
	}
	if err := os.MkdirAll(dirOf(s.path), 0o755); err != nil {
		return
	}
	if err := os.WriteFile(s.seedMarkerPath(), data, 0o644); err != nil {
		slog.Warn("写入告警规则播种记录失败", "err", err, "path", s.seedMarkerPath())
	}
}

// SeedDefaults 播种开箱即用的推荐规则。
//
// 播种的是 seedTemplates（DefaultTemplates 的子集）：只自动创建**跨环境普遍成立、
// 不需要用户先改阈值**的那十几条。模板库里的其余条目（约 40 条，如各类积压量、
// 连接数水位）保持"可选"，避免升级时把规则列表塞满。
//
// 播种以「规则名」为准做增量处理，并用一个独立的播种记录文件记住哪些内置规则已经播种过：
//   - 全新安装：全部写入；
//   - 版本升级：只补写本次版本新增、且此前从未播种过的内置规则（例如「PostgreSQL 服务离线」），
//     老版本已存在的规则不会重复创建；
//   - 用户主动删除过的内置规则不会被重新写回，因为它已记录在播种记录中。
func (s *RulesStore) SeedDefaults() {
	templates := seedTemplates()
	names := make([]string, 0, len(templates))
	for _, t := range templates {
		names = append(names, t.Name)
	}

	_, statErr := os.Stat(s.path)
	freshInstall := os.IsNotExist(statErr)

	seeded := s.loadSeedMarker()
	if freshInstall {
		// 全新安装：规则文件尚不存在，忽略可能残留的播种记录，全部写入
		seeded = map[string]bool{}
	}

	// 已存在的规则名，避免与用户自建的同名规则重复
	s.mu.RLock()
	existing := make(map[string]bool, len(s.rules))
	for _, r := range s.rules {
		existing[r.Name] = true
	}
	s.mu.RUnlock()

	added := 0
	for _, t := range templates {
		if seeded[t.Name] || existing[t.Name] {
			continue
		}
		s.Create(t)
		added++
	}
	if added > 0 {
		slog.Info("已补充内置告警规则", "count", added)
	}
	s.saveSeedMarker(names)
}

// sortRulesByCreatedDesc 按创建时间倒序排列（新建在前）；
// CreatedAt 相同时按 ID 倒序，保证顺序确定、不随 map 遍历随机抖动。
func sortRulesByCreatedDesc(out []model.AlertRule) {
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt > out[j].CreatedAt
		}
		return out[i].ID > out[j].ID
	})
}

// List 返回所有规则，按创建时间倒序。
func (s *RulesStore) List() []model.AlertRule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.AlertRule, 0, len(s.rules))
	for _, r := range s.rules {
		out = append(out, r)
	}
	sortRulesByCreatedDesc(out)
	return out
}

// Get 返回单个规则。
func (s *RulesStore) Get(id string) (model.AlertRule, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.rules[id]
	return r, ok
}

// Create 创建规则，自动生成 ID 与时间戳。
func (s *RulesStore) Create(r model.AlertRule) model.AlertRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := model.NowMillis()
	if r.ID == "" {
		r.ID = "r-" + strconv.FormatInt(now, 36)
	}
	r.CreatedAt = now
	r.UpdatedAt = now
	s.rules[r.ID] = r
	s.persistLocked()
	return r
}

// Update 更新规则。
func (s *RulesStore) Update(r model.AlertRule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.rules[r.ID]
	if !ok {
		return os.ErrNotExist
	}
	if r.CreatedAt == 0 {
		r.CreatedAt = existing.CreatedAt
	}
	r.UpdatedAt = model.NowMillis()
	s.rules[r.ID] = r
	s.persistLocked()
	return nil
}

// Delete 删除规则。
func (s *RulesStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.rules[id]; !ok {
		return os.ErrNotExist
	}
	delete(s.rules, id)
	s.persistLocked()
	return nil
}

// ImportRules 从外部导入规则。replace=true 时先清空现有规则再逐条创建；
// 否则按 ID 合并：已存在的规则更新其字段并保留原 CreatedAt，不存在的规则创建。
// 返回新增数与更新数，导入完成后一次性持久化。调用方无需持锁。
func (s *RulesStore) ImportRules(rules []model.AlertRule, replace bool) (created, updated int, err error) {
	seen := map[string]bool{}
	for _, r := range rules {
		if err := ValidateRule(r); err != nil {
			return 0, 0, err
		}
		if r.ID != "" {
			if seen[r.ID] {
				return 0, 0, fmt.Errorf("导入数据包含重复规则 ID: %s", r.ID)
			}
			seen[r.ID] = true
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if replace {
		s.rules = map[string]model.AlertRule{}
	}
	now := model.NowMillis()
	for _, r := range rules {
		if r.Name == "" {
			return created, updated, fmt.Errorf("存在名称为空的规则，已取消导入")
		}
		if r.ID == "" {
			r.ID = "r-" + strconv.FormatInt(now, 36)
		}
		if existing, ok := s.rules[r.ID]; ok {
			r.CreatedAt = existing.CreatedAt
			r.UpdatedAt = now
			s.rules[r.ID] = r
			updated++
		} else {
			if r.CreatedAt == 0 {
				r.CreatedAt = now
			}
			r.UpdatedAt = now
			s.rules[r.ID] = r
			created++
		}
	}
	s.persistLocked()
	return created, updated, nil
}

func (s *RulesStore) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var list []model.AlertRule
	if err := yaml.Unmarshal(data, &list); err != nil {
		// 备份损坏文件（通常为旧版本残留格式），避免内容静默丢失；
		// 下次通过界面保存规则时会以新格式覆盖原文件。
		backup := fmt.Sprintf("%s.corrupt-%d", s.path, time.Now().Unix())
		if werr := os.WriteFile(backup, data, 0o644); werr == nil {
			slog.Warn("解析规则文件失败，已备份为损坏文件", "path", s.path, "backup", backup, "err", err)
		} else {
			slog.Warn("解析规则文件失败", "path", s.path, "err", err, "backupErr", werr)
		}
		return
	}
	for _, r := range list {
		if r.ID == "" {
			r.ID = "r-" + strconv.FormatInt(r.CreatedAt, 36)
		}
		s.rules[r.ID] = r
	}
}

func (s *RulesStore) persistLocked() {
	list := make([]model.AlertRule, 0, len(s.rules))
	for _, r := range s.rules {
		list = append(list, r)
	}
	sortRulesByCreatedDesc(list)
	data, err := yaml.Marshal(list)
	if err != nil {
		slog.Warn("序列化规则失败", "err", err)
		return
	}
	if err := os.MkdirAll(dirOf(s.path), 0o755); err != nil {
		slog.Warn("创建规则目录失败", "err", err)
		return
	}
	if err := config.AtomicWrite(s.path, data); err != nil {
		slog.Warn("写入规则文件失败", "err", err, "path", s.path)
	}
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[:i]
		}
	}
	return "."
}

// parseFor 将 "5m" 之类解析为秒。
func parseFor(s string) int64 {
	if s == "" || s == "0" || s == "0s" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		// 非法持续时间不能退化为立即触发，否则配置拼写错误会造成误报风暴。
		return 1 << 62
	}
	return int64(d.Seconds())
}

// ValidateRule 校验来自 API/导入的数据，避免非法规则被持久化后以“立即触发”运行。
func ValidateRule(r model.AlertRule) error {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("规则名称不能为空")
	}
	switch r.Severity {
	case model.SeverityCritical, model.SeverityWarning, model.SeverityInfo:
	default:
		return fmt.Errorf("无效告警级别: %q", r.Severity)
	}
	switch r.Type {
	case "", "threshold":
		if r.Metric == "" {
			return fmt.Errorf("阈值规则必须设置指标")
		}
		if !validOperator(r.Operator) {
			return fmt.Errorf("无效运算符: %q", r.Operator)
		}
	case model.RuleTypeNodeOffline:
	case model.RuleTypeServiceDown, model.RuleTypeRoleChange, model.RuleTypeClusterFault:
		if !validService(r.Service) {
			return fmt.Errorf("无效中间件类型: %q", r.Service)
		}
	case model.RuleTypeSecurityEvent:
		if r.Category != "" && !validSecurityCategory(r.Category) {
			return fmt.Errorf("无效安全事件类别: %q", r.Category)
		}
	default:
		return fmt.Errorf("无效规则类型: %q", r.Type)
	}
	if r.Scope != "" && r.Scope != "all" && r.Scope != "specified" {
		return fmt.Errorf("无效作用范围: %q", r.Scope)
	}
	if r.Scope == "specified" && len(r.Nodes) == 0 {
		return fmt.Errorf("指定主机范围不能为空")
	}
	if r.For != "" && r.For != "0" {
		d, err := time.ParseDuration(r.For)
		if err != nil || d < 0 {
			return fmt.Errorf("无效持续时间: %q", r.For)
		}
	}
	validChannels := map[string]bool{"email": true, "webhook": true, "dingtalk": true, "feishu": true, "wecom": true}
	for _, ch := range r.Notify {
		if !validChannels[ch] {
			return fmt.Errorf("无效通知渠道: %q", ch)
		}
	}
	for _, q := range r.QuietPeriods {
		if parseHHMM(q.Start) < 0 || parseHHMM(q.End) < 0 {
			return fmt.Errorf("无效静默时间: %q-%q", q.Start, q.End)
		}
	}
	if r.Escalation != nil {
		if r.Escalation.AfterMinutes < 0 || r.Escalation.RepeatMinutes < 0 {
			return fmt.Errorf("升级时间不能为负数")
		}
		for _, ch := range r.Escalation.Channels {
			if !validChannels[ch] {
				return fmt.Errorf("无效升级通知渠道: %q", ch)
			}
		}
	}
	return nil
}

func validOperator(op string) bool {
	switch op {
	case ">", ">=", "<", "<=", "==", "!=":
		return true
	default:
		return false
	}
}

// KnownServices 是**内置**服务类型清单。
//
// 模板派生类型（由采集项模板生成的中间件类型）不在此列——它们由注册表在运行期提供，
// 见 SetMiddlewareRegistry 与 validService。保留本变量是为了让「内置清单」有明确出处，
// 并供测试遍历（内置部分必须与指标目录一致）。
var KnownServices = []string{
	"mysql", "postgres", "redis", "nginx", "kafka", "rocketmq", "docker", "k8s", "mongodb", "fastdfs",
	// 下面 5 类此前只有采集器与 mwreg 注册表，漏登记在这里——于是 TestServiceMetricNamesAreRegistered
	// 也就没在守它们的存活指标（清单与注册表脱节的症状是"测试看起来很全，其实少守了 5 类"）。
	"rabbitmq", "elasticsearch", "clickhouse", "nacos", "zookeeper",
}

// validService 校验「服务离线 / 主从切换 / 集群故障」规则的服务类型是否已知。
//
// 走注册表而非上面的内置清单：模板派生类型也应能被这些规则监控
// （否则「新增中间件只写模板」到告警这一步就断了）。
func validService(s string) bool {
	return mwTypes.Has(s)
}

func validSecurityCategory(category string) bool {
	switch category {
	case model.SecurityCatSSHBruteforce, model.SecurityCatSSHAudit, model.SecurityCatFIM,
		model.SecurityCatProcessAnomaly, model.SecurityCatSudoAudit, model.SecurityCatBan:
		return true
	default:
		return false
	}
}
