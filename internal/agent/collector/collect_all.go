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
	MongoDB     []model.MongoDBInstance
	FastDFS     []model.FastDFSInstance

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
			m, inst := c.k8s.CollectCtx(ctx)
			addMetrics(m)
			res.K8s = inst
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

	// 采集项模板：每个模板一个任务，直接继承 per-task 超时与失败隔离
	// （一个模板卡住/写坏不应拖累其它模板与主机采集）。
	// 模板集每轮现取（下发可随时替换），为空时不追加任何任务——无模板的节点与改造前完全等价。
	if tpls, runner := c.templateState(); runner != nil {
		for _, tpl := range tpls {
			tpl := tpl
			tasks = append(tasks, collectTask{name: "template:" + tpl.ID, run: func(ctx context.Context) error {
				addMetrics(runner.CollectTemplate(ctx, tpl))
				return nil
			}})
		}
	}
	return tasks
}
