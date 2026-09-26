package collector

import (
	"context"
	"testing"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/template"
)

// 本文件是 E2「用真实中间件走一遍」的证据：把内置预设喂给**按真实 exporter 输出形态构造的样本**，
// 断言预设规则确实能挑出该中间件的指标、挡住噪声，并且不会产出重复序列。
//
// 样本来源说明（诚实起见写清楚）：这些样本依据各 exporter 的官方文档与常见输出形态构造，
// 不是从某个真实集群抓取。它们刻意覆盖了实际会遇到的几类要素——
// HELP/TYPE 注释行、*_created 时间戳序列、直方图 bucket、维度标签（queue/index）、
// 以及 exporter 自身的 go_*/process_* 运行指标。真实环境的差异（指标增删、标签变化）
// 不会影响这些规则的正确性判断，但「该中间件有没有暴露某指标」仍需以现场为准。

const rabbitmqSample = `# HELP rabbitmq_queues_total Total number of queues
# TYPE rabbitmq_queues_total gauge
rabbitmq_queues_total{rabbitmq_node="rabbit@node1"} 12
rabbitmq_queue_messages{queue="orders",vhost="/"} 42
rabbitmq_queue_messages{queue="payments",vhost="/"} 7
rabbitmq_queue_consumers{queue="orders",vhost="/"} 2
rabbitmq_queue_messages_created{queue="orders",vhost="/"} 1700000000
rabbitmq_process_resident_memory_bytes{rabbitmq_node="rabbit@node1"} 1.2e+08
erlang_vm_process_count 42
go_goroutines 15
`

const clickhouseSample = `# TYPE ClickHouseProfileEvents_Query counter
ClickHouseProfileEvents_Query 1234
ClickHouseProfileEvents_QueryMemoryUsage 512
ClickHouseProfileEvents_Query_created 1700000000
ClickHouseMetrics_Query 5
ClickHouseAsyncMetrics_Uptime 3600
go_goroutines 10
`

const etcdSample = `etcd_server_has_leader 1
etcd_mvcc_db_total_size_in_bytes 8.4e+07
etcd_disk_wal_fsync_duration_seconds_bucket{le="0.001"} 100
etcd_disk_wal_fsync_duration_seconds_bucket{le="+Inf"} 200
etcd_disk_wal_fsync_duration_seconds_sum 0.5
etcd_disk_wal_fsync_duration_seconds_count 200
grpc_server_started_total 5
`

const elasticsearchSample = `elasticsearch_cluster_health_status{color="green"} 1
elasticsearch_jvm_memory_used_bytes{area="heap"} 1.1e+09
elasticsearch_indices_docs{index="logs"} 1000
process_cpu_seconds_total 1
`

const zookeeperSample = `zk_server_state{state="leader"} 1
zk_num_alive_connections 8
zk_avg_latency 0.4
zk_znode_count 130
jmx_exporter_build_info 1
`

// TestPresets_RealWorldPayloads 逐个预设核对产出：该留的留下、该挡的挡住。
func TestPresets_RealWorldPayloads(t *testing.T) {
	cases := []struct {
		id        string
		body      string
		wantNames []string // 期望产出（已是加了模板前缀后的最终名）
		notWant   []string
	}{
		{
			id:   "rabbitmq",
			body: rabbitmqSample,
			// id 与指标族同名，EnsurePrefix 不应重复加前缀
			wantNames: []string{"rabbitmq_queues_total", "rabbitmq_queue_messages", "rabbitmq_queue_consumers", "rabbitmq_process_resident_memory_bytes"},
			// 噪声：*_created、erlang VM、exporter 自身的 go_*
			notWant: []string{"rabbitmq_queue_messages_created", "erlang_vm_process_count", "go_goroutines"},
		},
		{
			id:        "clickhouse",
			body:      clickhouseSample,
			wantNames: []string{"clickhouse_events_Query", "clickhouse_events_QueryMemoryUsage", "clickhouse_metrics_Query", "clickhouse_async_Uptime"},
			notWant:   []string{"clickhouse_events_Query_created", "go_goroutines"},
		},
		{
			id:        "etcd",
			body:      etcdSample,
			wantNames: []string{"etcd_server_has_leader", "etcd_mvcc_db_total_size_in_bytes", "etcd_disk_wal_fsync_duration_seconds_sum", "etcd_disk_wal_fsync_duration_seconds_count"},
			// 直方图 bucket 基数高且本项目没有分位数口径，预设置为丢弃
			notWant: []string{"etcd_disk_wal_fsync_duration_seconds_bucket", "grpc_server_started_total"},
		},
		{
			id:        "elasticsearch",
			body:      elasticsearchSample,
			wantNames: []string{"elasticsearch_cluster_health_status", "elasticsearch_jvm_memory_used_bytes", "elasticsearch_indices_docs"},
			notWant:   []string{"process_cpu_seconds_total"},
		},
		{
			id:        "zk",
			body:      zookeeperSample,
			wantNames: []string{"zk_server_state", "zk_num_alive_connections", "zk_avg_latency", "zk_znode_count"},
			notWant:   []string{"jmx_exporter_build_info", "zookeeper_zk_server_state"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			p, ok := template.PresetByID(tc.id)
			if !ok {
				t.Fatalf("找不到预设 %s", tc.id)
			}
			cfg := p.Config
			srv := promServer(t, tc.body)
			cfg.Targets = []template.Target{{Addr: srv.URL}}

			if err := template.ValidateAll([]template.Config{cfg}); err != nil {
				t.Fatalf("预设 %s 未通过校验：%v", tc.id, err)
			}

			got := NewTemplateRunner("test-node").CollectTemplate(context.Background(), cfg)
			byN := byName(got)

			if got := byN[template.UpMetricName]; len(got) != 1 || got[0].Value != 1 {
				t.Fatalf("应产出 up=1：%+v", got)
			}
			for _, name := range tc.wantNames {
				if len(byN[name]) == 0 {
					t.Errorf("缺少期望指标 %s（实际产出：%v）", name, namesOf(got))
				}
			}
			for _, name := range tc.notWant {
				if len(byN[name]) > 0 {
					t.Errorf("不应产出 %s（预设的 keep/drop 未挡住）", name)
				}
			}
			assertNoDuplicateSeries(t, got)
		})
	}
}

// TestPresets_RabbitMQKeepsQueueDimension 默认保留队列维度：多维指标是「能定位到哪个队列」的前提。
func TestPresets_RabbitMQKeepsQueueDimension(t *testing.T) {
	p, _ := template.PresetByID("rabbitmq")
	cfg := p.Config
	srv := promServer(t, rabbitmqSample)
	cfg.Targets = []template.Target{{Addr: srv.URL}}

	got := byName(NewTemplateRunner("test-node").CollectTemplate(context.Background(), cfg))["rabbitmq_queue_messages"]
	if len(got) != 2 {
		t.Fatalf("两个队列应产出两条序列，got %d", len(got))
	}
	if got[0].Labels["queue"] != "orders" || got[0].Value != 42 {
		t.Fatalf("队列维度或取值不符：%+v", got[0])
	}
	if got[1].Labels["vhost"] != "/" {
		t.Fatalf("vhost 标签应保留：%+v", got[1])
	}
}

// TestTemplate_UnlabelWithoutAggregateOnlyKeepsOne 未声明聚合就丢掉区分序列的标签时，
// 只保留一条并告警——绝不能把多条「同名同标签」序列一起写进时序库（那是 last-write-wins 的静默损坏）。
func TestTemplate_UnlabelWithoutAggregateOnlyKeepsOne(t *testing.T) {
	p, _ := template.PresetByID("rabbitmq")
	cfg := p.Config
	srv := promServer(t, rabbitmqSample)
	cfg.Targets = []template.Target{{Addr: srv.URL}}
	// 这是最容易写出错的配置：想汇总所有队列，于是把 queue 标签删掉
	cfg.Rules.Unlabel = []string{"queue", "vhost"}

	got := byName(NewTemplateRunner("test-node").CollectTemplate(context.Background(), cfg))
	msgs := got["rabbitmq_queue_messages"]
	if len(msgs) != 1 {
		t.Fatalf("同名同标签的多条序列只应保留一条，got %d 条（其余会互相覆盖）", len(msgs))
	}
	// 保留顺序确定：取响应中的第一条（orders=42），不随 map 遍历变化
	if msgs[0].Value != 42 {
		t.Fatalf("应稳定保留第一条（42），got %v", msgs[0].Value)
	}
}

// TestTemplate_AggregateSumsAcrossLabel 声明聚合后才能真正表达「所有队列的消息总数」。
func TestTemplate_AggregateSumsAcrossLabel(t *testing.T) {
	p, _ := template.PresetByID("rabbitmq")
	cfg := p.Config
	srv := promServer(t, rabbitmqSample)
	cfg.Targets = []template.Target{{Addr: srv.URL}}
	cfg.Rules.Unlabel = []string{"queue", "vhost"}
	cfg.Rules.Aggregate = []template.AggregateRule{{Match: "^rabbitmq_queue_messages$", Op: template.AggSum}}

	got := byName(NewTemplateRunner("test-node").CollectTemplate(context.Background(), cfg))
	msgs := got["rabbitmq_queue_messages"]
	if len(msgs) != 1 {
		t.Fatalf("聚合后应只有一条序列，got %d", len(msgs))
	}
	if msgs[0].Value != 49 {
		t.Fatalf("sum 聚合应得 42+7=49，got %v", msgs[0].Value)
	}
	// 未声明聚合的指标不受影响：consumer 数仍按队列分开（此处被 unlabel 掉维度，故只剩一条）
	if len(got["rabbitmq_queues_total"]) != 1 {
		t.Fatalf("未匹配聚合规则的指标应原样保留：%+v", got["rabbitmq_queues_total"])
	}
}

// TestTemplate_AggregateOps 四种聚合方式都要正确（avg 尤其容易写错成「和为 0 时除零」）。
func TestTemplate_AggregateOps(t *testing.T) {
	body := "mq_value{shard=\"a\"} 10\nmq_value{shard=\"b\"} 20\nmq_value{shard=\"c\"} 0\n"
	cases := map[string]float64{
		template.AggSum: 30,
		template.AggMax: 20,
		template.AggMin: 0,
		template.AggAvg: 10,
	}
	for op, want := range cases {
		t.Run(op, func(t *testing.T) {
			cfg := template.Config{
				ID:      "mq",
				Kind:    template.KindPrometheusExporter,
				Targets: []template.Target{{Addr: promServer(t, body).URL}},
				Rules: template.Rules{
					Unlabel:   []string{"shard"},
					Aggregate: []template.AggregateRule{{Match: "^mq_value$", Op: op}},
				},
			}
			got := byName(NewTemplateRunner("test-node").CollectTemplate(context.Background(), cfg))["mq_value"]
			if len(got) != 1 {
				t.Fatalf("聚合后应只有一条序列，got %d", len(got))
			}
			if got[0].Value != want {
				t.Fatalf("op=%s 结果 = %v，want %v", op, got[0].Value, want)
			}
		})
	}
}

// assertNoDuplicateSeries 断言不存在「同名 + 同标签」的重复序列。
func assertNoDuplicateSeries(t *testing.T, ms []model.Metric) {
	t.Helper()
	seen := map[string]int{}
	for _, m := range ms {
		seen[seriesKey(m)]++
	}
	for key, n := range seen {
		if n > 1 {
			// key 里含不可见分隔符，只打印可读部分
			t.Errorf("存在重复序列（%d 条）：%s", n, key[:min(len(key), 60)])
		}
	}
}
