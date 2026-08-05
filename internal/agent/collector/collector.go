// Package collector 实现主机指标采集。各采集器独立、可开关，结果统一为 model.Metric。
package collector

import (
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

	cpu    *CPUCollector
	disk   *DiskCollector
	net    *NetworkCollector
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
) *Collector {
	c := &Collector{
		node:   node,
		group:  group,
		labels: labels,
		cfg:    cfg,
		cpu:    NewCPUCollector(),
		disk:   NewDiskCollector(),
		net:    NewNetworkCollector(),
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
	return c
}

// Collect 采集所有启用指标，并填充节点基础信息。
// 返回的 metrics 已带 node 标签；group 由 Server 在写入时补充。
func (c *Collector) Collect() ([]model.Metric, []model.ProcessStat) {
	now := model.NowMillis()
	var metrics []model.Metric
	add := func(name string, value float64, labels map[string]string) {
		m := model.Metric{Node: c.node, Name: name, Value: value, Timestamp: now}
		if labels != nil {
			m.Labels = labels
		}
		metrics = append(metrics, m)
	}

	if c.cfg.CPU {
		for _, m := range c.cpu.Collect() {
			add(m.Name, m.Value, m.Labels)
		}
	}
	if c.cfg.Memory {
		for _, m := range collectMemory() {
			add(m.Name, m.Value, m.Labels)
		}
	}
	if c.cfg.Load {
		for _, m := range collectLoad() {
			add(m.Name, m.Value, m.Labels)
		}
	}
	if c.cfg.Disk {
		for _, m := range c.disk.Collect() {
			add(m.Name, m.Value, m.Labels)
		}
	}
	if c.cfg.Network {
		for _, m := range c.net.Collect() {
			add(m.Name, m.Value, m.Labels)
		}
	}
	if c.cfg.Port && c.port != nil {
		for _, m := range c.port.Collect() {
			add(m.Name, m.Value, m.Labels)
		}
	}

	var procs []model.ProcessStat
	if c.cfg.Process {
		var total int
		procs, total = collectProcessTop()
		// 进程总数单独上报为时序指标，供大屏「进程总数」等聚合展示使用
		add("process_total", float64(total), nil)
	}
	return metrics, procs
}

// CollectRedis 采集 Redis 指标，返回 redis_* 指标与实例元信息。
// 调用方负责将 metrics 合并到 ReportPayload.Metrics，instances 填入 ReportPayload.RedisInstances。
func (c *Collector) CollectRedis() ([]model.Metric, []model.RedisInstance) {
	if c.redis == nil {
		return nil, nil
	}
	return c.redis.Collect()
}

// CollectMySQL 采集 MySQL 指标。
func (c *Collector) CollectMySQL() ([]model.Metric, []model.MySQLInstance) {
	if c.mysql == nil {
		return nil, nil
	}
	return c.mysql.Collect()
}

// CollectPostgres 采集 PostgreSQL 指标。
func (c *Collector) CollectPostgres() ([]model.Metric, []model.PostgresInstance) {
	if c.pg == nil {
		return nil, nil
	}
	return c.pg.Collect()
}

// CollectNginx 采集 Nginx 指标。
func (c *Collector) CollectNginx() ([]model.Metric, []model.NginxInstance) {
	if c.nginx == nil {
		return nil, nil
	}
	return c.nginx.Collect()
}

// CollectNginxAccess 采集 Nginx access log 聚合统计（每实例一条）。
func (c *Collector) CollectNginxAccess() []model.NginxAccessStat {
	if c.nginxAccess == nil {
		return nil
	}
	return c.nginxAccess.Collect()
}

// CollectKafka 采集 Kafka 指标。
func (c *Collector) CollectKafka() ([]model.Metric, []model.KafkaInstance) {
	if c.kafka == nil {
		return nil, nil
	}
	return c.kafka.Collect()
}

// CollectDocker 采集 Docker 容器指标。
func (c *Collector) CollectDocker() ([]model.Metric, []model.DockerInstance) {
	if c.docker == nil {
		return nil, nil
	}
	return c.docker.Collect()
}

// CollectRocketMQ 采集 RocketMQ 指标。
func (c *Collector) CollectRocketMQ() ([]model.Metric, []model.RocketMQInstance) {
	if c.rmq == nil {
		return nil, nil
	}
	return c.rmq.Collect()
}

// CollectK8s 采集 Kubernetes 集群指标。
func (c *Collector) CollectK8s() ([]model.Metric, []model.K8sInstance) {
	if c.k8s == nil {
		return nil, nil
	}
	return c.k8s.Collect()
}

// CollectMongoDB 采集 MongoDB 指标。
func (c *Collector) CollectMongoDB() ([]model.Metric, []model.MongoDBInstance) {
	if c.mongo == nil {
		return nil, nil
	}
	return c.mongo.Collect()
}

// CollectFastDFS 采集 FastDFS 指标。
func (c *Collector) CollectFastDFS() ([]model.Metric, []model.FastDFSInstance) {
	if c.fastdfs == nil {
		return nil, nil
	}
	return c.fastdfs.Collect()
}

// HostInfo 返回主机静态信息（OS/Arch/IP），用于上报体。
func (c *Collector) HostInfo() (os, arch, ip string) {
	info, err := host.Info()
	if err == nil {
		os = info.OS + " " + info.Platform + " " + info.PlatformVersion
	}
	arch = hostArch()
	ip = primaryIP()
	return
}
