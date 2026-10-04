package collector

import (
	"context"
	"sync"

	"github.com/nebula/monitor/internal/model"
)

// Result 是一轮采集的完整产出。
//
// 各字段由对应采集任务写入，任务之间互不重叠；Metrics 为所有来源指标的汇总，
// 由各任务在锁内追加（唯一的共享 slice）。
type Result struct {
	Metrics   []model.Metric
	Processes []model.ProcessStat

	Redis       []model.RedisInstance
	MySQL       []model.MySQLInstance
	Postgres    []model.PostgresInstance
	Nginx       []model.NginxInstance
	NginxAccess []model.NginxAccessStat
	Kafka       []model.KafkaInstance
	Docker      []model.DockerInstance
	RocketMQ    []model.RocketMQInstance
	K8s         []model.K8sInstance
	// K8s 台账清单：Pod 与工作负载（见 docs/superpowers/specs/2026-10-04-container-inventory-design.md）。
	// 与 K8s（集群元信息）分开：集群是"这个集群在不在"，清单是"里面有东西在跑"。
	K8sPods      []model.K8sPod
	K8sWorkloads []model.K8sWorkload
	// 清单达到单轮上限被截断——必须传到上报体，否则中心会把"1000 个 Pod"当成全部。
	K8sPodsTruncated      bool
	K8sWorkloadsTruncated bool
	MongoDB               []model.MongoDBInstance
	FastDFS               []model.FastDFSInstance
	RabbitMQ              []model.RabbitMQInstance
	Elasticsearch         []model.ElasticsearchInstance
	ClickHouse            []model.ClickHouseInstance
	Nacos                 []model.NacosInstance
	ZooKeeper             []model.ZooKeeperInstance

	SecurityEvents   []model.SecurityEvent
	SecurityBaseline *model.SecurityBaseline

	Listeners      []model.ListenerStat
	FirewallRules  []model.FirewallRule
	FirewallStatus *model.FirewallStatus

	OS       string
	Arch     string
	IP       string
	HostInfo model.HostInfo
}

// CollectAll 并发执行一轮完整采集并返回聚合结果。
//
//   - 任务级超时取 Collector 的 collectTimeout（0 表示不限制）；
//   - 任一来源失败或超时只影响自身，不阻断其它来源（见 runTasks 的语义约定）；
//   - 返回时保证全部任务已结束，不遗留 goroutine。
func (c *Collector) CollectAll(ctx context.Context) Result {
	var res Result
	var mu sync.Mutex
	runTasks(ctx, c.timeout, c.tasks(&res, &mu))
	return res
}

// tasks 构建本轮采集任务清单。
//
// 说明：防火墙「规则」与「状态」合并为同一个任务——状态中的 ruleCount 需要规则条数，
// 且两者各自会 exec 探测同一批防火墙后端命令，合并后避免同一轮重复探测。
func (c *Collector) tasks(res *Result, mu *sync.Mutex) []collectTask {
	// addMetrics 在锁内追加指标，避免多个任务并发写同一个 slice。
	addMetrics := func(metrics []model.Metric) {
		if len(metrics) == 0 {
			return
		}
		mu.Lock()
		res.Metrics = append(res.Metrics, metrics...)
		mu.Unlock()
	}

	tasks := []collectTask{
		{name: "host", run: func(ctx context.Context) error {
			m, procs := c.CollectCtx(ctx)
			addMetrics(m)
			res.Processes = procs
			return nil
		}},
		{name: "redis", run: func(ctx context.Context) error {
			if c.redis == nil {
				return nil
			}
			m, inst := c.redis.CollectCtx(ctx)
			addMetrics(m)
			res.Redis = inst
			return nil
		}},
		{name: "mysql", run: func(ctx context.Context) error {
			if c.mysql == nil {
				return nil
			}
			m, inst := c.mysql.CollectCtx(ctx)
			addMetrics(m)
			res.MySQL = inst
			return nil
		}},
		{name: "postgres", run: func(ctx context.Context) error {
			if c.pg == nil {
				return nil
			}
			m, inst := c.pg.CollectCtx(ctx)
			addMetrics(m)
			res.Postgres = inst
			return nil
		}},
		{name: "nginx", run: func(ctx context.Context) error {
			if c.nginx == nil {
				return nil
			}
			m, inst := c.nginx.CollectCtx(ctx)
			addMetrics(m)
			res.Nginx = inst
			return nil
		}},
		{name: "nginx-access", run: func(ctx context.Context) error {
			if c.nginxAccess == nil {
				return nil
			}
			res.NginxAccess = c.nginxAccess.CollectCtx(ctx)
			return nil
		}},
		{name: "kafka", run: func(ctx context.Context) error {
			if c.kafka == nil {
				return nil
			}
			m, inst := c.kafka.CollectCtx(ctx)
			addMetrics(m)
			res.Kafka = inst
			return nil
		}},
		{name: "docker", run: func(ctx context.Context) error {
			if c.docker == nil {
				return nil
			}
			m, inst := c.docker.CollectCtx(ctx)
			addMetrics(m)
			res.Docker = inst
			return nil
		}},
		{name: "rocketmq", run: func(ctx context.Context) error {
			if c.rmq == nil {
				return nil
			}
			m, inst := c.rmq.CollectCtx(ctx)
			addMetrics(m)
			res.RocketMQ = inst
			return nil
		}},
		{name: "k8s", run: func(ctx context.Context) error {
			if c.k8s == nil {
				return nil
			}
			r := c.k8s.CollectCtx(ctx)
			addMetrics(r.Metrics)
			res.K8s = r.Instances
			res.K8sPods = r.Pods
			res.K8sWorkloads = r.Workloads
			res.K8sPodsTruncated = r.PodsTruncated
			res.K8sWorkloadsTruncated = r.WorkloadsTruncated
			return nil
		}},
		{name: "mongodb", run: func(ctx context.Context) error {
			if c.mongo == nil {
				return nil
			}
			m, inst := c.mongo.CollectCtx(ctx)
			addMetrics(m)
			res.MongoDB = inst
			return nil
		}},
		{name: "fastdfs", run: func(ctx context.Context) error {
			if c.fastdfs == nil {
				return nil
			}
			m, inst := c.fastdfs.CollectCtx(ctx)
			addMetrics(m)
			res.FastDFS = inst
			return nil
		}},
		{name: "rabbitmq", run: func(ctx context.Context) error {
			if c.rabbitmq == nil {
				return nil
			}
			m, inst := c.rabbitmq.CollectCtx(ctx)
			addMetrics(m)
			res.RabbitMQ = inst
			return nil
		}},
		{name: "elasticsearch", run: func(ctx context.Context) error {
			if c.es == nil {
				return nil
			}
			m, inst := c.es.CollectCtx(ctx)
			addMetrics(m)
			res.Elasticsearch = inst
			return nil
		}},
		{name: "clickhouse", run: func(ctx context.Context) error {
			if c.clickhouse == nil {
				return nil
			}
			m, inst := c.clickhouse.CollectCtx(ctx)
			addMetrics(m)
			res.ClickHouse = inst
			return nil
		}},
		{name: "nacos", run: func(ctx context.Context) error {
			if c.nacos == nil {
				return nil
			}
			m, inst := c.nacos.CollectCtx(ctx)
			addMetrics(m)
			res.Nacos = inst
			return nil
		}},
		{name: "zookeeper", run: func(ctx context.Context) error {
			if c.zookeeper == nil {
				return nil
			}
			m, inst := c.zookeeper.CollectCtx(ctx)
			addMetrics(m)
			res.ZooKeeper = inst
			return nil
		}},
		{name: "security", run: func(ctx context.Context) error {
			if c.security == nil {
				return nil
			}
			res.SecurityEvents, res.SecurityBaseline = c.security.CollectCtx(ctx)
			return nil
		}},
		{name: "listeners", run: func(ctx context.Context) error {
			res.Listeners = c.CollectListenersCtx(ctx)
			return nil
		}},
		{name: "firewall", run: func(ctx context.Context) error {
			rules := c.CollectFirewallRulesCtx(ctx)
			res.FirewallRules = rules
			res.FirewallStatus = c.CollectFirewallStatusCtx(ctx, len(rules))
			return nil
		}},
		{name: "host-info", run: func(ctx context.Context) error {
			res.OS, res.Arch, res.IP = c.HostInfoCtx(ctx)
			res.HostInfo = CollectHostInfoCtx(ctx)
			return nil
		}},
	}

	// 集中日志（C2）：单独一个任务（同样继承 per-task 超时与失败隔离）。
	// 未配置 logSources 时 c.logs 为 nil，不追加任务——与改造前完全等价。
	if c.logs != nil {
		tasks = append(tasks, collectTask{name: "logs", run: func(ctx context.Context) error {
			addMetrics(c.logs.CollectCtx(ctx))
			return nil
		}})
	}
	return tasks
}
