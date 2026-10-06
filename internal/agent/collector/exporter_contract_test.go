package collector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/model"
)

// expositionServer 每次请求都返回同一段文本；正文由参数固化，避免测试主协程与
// handler 协程共享可变状态（CI 会对本包跑 -race）。
func expositionServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// upstreamDownCases 覆盖各类型第三方 exporter 的真实存活指标名。
//
// 平台自己的目录名是 `<mw>_instance_up`（由直连采集器或 receiver 产出），而第三方
// exporter 暴露的是另一套名字；只看平台名会让 `*_up 0` 被回退分支判成在线。
func upstreamDownCases() map[string]string {
	return map[string]string{
		"mysql":    "mysql_up 0\nmysql_threads_connected 3\n",
		"postgres": "pg_up 0\npg_stat_database_numbackends 3\n",
		"nginx":    "nginx_up 0\nnginx_connections_active 3\n",
		"redis":    "redis_up 0\nredis_connected_clients 3\n",
		"rabbitmq": "rabbitmq_up 0\nrabbitmq_connections 3\n",
		"mongodb":  "mongodb_up 0\nmongodb_connections_current 3\n",
	}
}

func TestMySQLExporterHealthFollowsExposition(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{name: "empty"},
		{name: "comment only", body: "# HELP mysql_up whether the target is up\n# TYPE mysql_up gauge\n"},
		{name: "unrelated", body: "process_cpu_seconds_total 1\n"},
		{name: "platform up name down", body: "mysql_instance_up 0\nmysql_threads_connected 3\n"},
		{name: "upstream up name down", body: "mysql_up 0\nmysql_threads_connected 3\n"},
		{name: "upstream up name up", body: "mysql_up 1\n", want: true},
		{name: "business fallback", body: "mysql_threads_connected 3\n", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := expositionServer(t, tc.body)
			collector := NewMySQLCollector("node-a", []model.MySQLInstanceConfig{{Name: "mysql", Addr: "db:3306", ExporterURL: srv.URL}})
			_, instances := collector.CollectCtx(context.Background())
			if len(instances) != 1 || instances[0].Up != tc.want {
				t.Fatalf("instances = %+v, want up=%v", instances, tc.want)
			}
		})
	}
}

// 平台目录名与上游惯用名同时出现时，平台口径（`<mw>_instance_up`）必须优先：
// 它描述的是「我们配置的这个实例」，而 `mysql_up` 可能来自 exporter 的其它 target。
// 顺序无关紧要——两种排列都要以平台口径为准，否则会出现「实例其实离线却报在线」。
func TestExporterHealthPrefersPlatformInstanceUp(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{name: "platform down first", body: "mysql_instance_up 0\nmysql_up 1\nmysql_threads_connected 3\n"},
		{name: "platform down last", body: "mysql_up 1\nmysql_instance_up 0\nmysql_threads_connected 3\n"},
		{name: "platform up, upstream down", body: "mysql_up 0\nmysql_instance_up 1\n", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := expositionServer(t, tc.body)
			collector := NewMySQLCollector("node-a", []model.MySQLInstanceConfig{{Name: "mysql", Addr: "db:3306", ExporterURL: srv.URL}})
			_, instances := collector.CollectCtx(context.Background())
			if len(instances) != 1 || instances[0].Up != tc.want {
				t.Fatalf("instances = %+v, want up=%v（body=%q）", instances, tc.want, tc.body)
			}
		})
	}
}

// 各类型第三方 exporter 报出的 `*_up 0` 必须把实例判为离线。
func TestExporterUpstreamDownMetricStaysOffline(t *testing.T) {
	down := upstreamDownCases()
	collect := map[string]func(string) bool{
		"mysql": func(url string) bool {
			_, xs := NewMySQLCollector("node", []model.MySQLInstanceConfig{{Name: "x", Addr: "x:3306", ExporterURL: url}}).CollectCtx(context.Background())
			return len(xs) == 1 && xs[0].Up
		},
		"postgres": func(url string) bool {
			_, xs := NewPostgresCollector("node", []model.PostgresInstanceConfig{{Name: "x", Addr: "x:5432", ExporterURL: url}}).CollectCtx(context.Background())
			return len(xs) == 1 && xs[0].Up
		},
		"nginx": func(url string) bool {
			_, xs := NewNginxCollector("node", []model.NginxInstanceConfig{{Name: "x", Addr: "x:80", ExporterURL: url}}).CollectCtx(context.Background())
			return len(xs) == 1 && xs[0].Up
		},
		"redis": func(url string) bool {
			_, xs := NewRedisCollector("node", []model.RedisInstanceConfig{{Name: "x", Addr: "x:6379", ExporterURL: url}}).CollectCtx(context.Background())
			return len(xs) == 1 && xs[0].Up
		},
		"rabbitmq": func(url string) bool {
			_, xs := NewRabbitMQCollector("node", []model.RabbitMQInstanceConfig{{Name: "x", Addr: strings.TrimPrefix(url, "http://")}}).CollectCtx(context.Background())
			return len(xs) == 1 && xs[0].Up
		},
		"mongodb": func(url string) bool {
			_, xs := NewMongoDBCollector("node", []model.MongoDBInstanceConfig{{Name: "x", Addr: "x:27017", ExporterURL: url}}).CollectCtx(context.Background())
			return len(xs) == 1 && xs[0].Up
		},
	}
	for name, body := range down {
		t.Run(name, func(t *testing.T) {
			collector, ok := collect[name]
			if !ok {
				t.Fatalf("missing collector for %s", name)
			}
			if collector(expositionServer(t, body).URL) {
				t.Fatalf("upstream down metric reported online: %q", body)
			}
		})
	}
}

func TestOtherExporterCollectorsRequireExpectedMetrics(t *testing.T) {
	cases := []struct {
		name   string
		metric string
		check  func(string) bool
	}{
		{name: "kafka", metric: "kafka_brokers 1\n", check: func(url string) bool {
			_, xs := NewKafkaCollector("node", []model.KafkaInstanceConfig{{Name: "x", Addr: "x:9092", ExporterURL: url}}).CollectCtx(context.Background())
			return len(xs) == 1 && xs[0].Up
		}},
		{name: "postgres", metric: "postgres_connections 1\n", check: func(url string) bool {
			_, xs := NewPostgresCollector("node", []model.PostgresInstanceConfig{{Name: "x", Addr: "x:5432", ExporterURL: url}}).CollectCtx(context.Background())
			return len(xs) == 1 && xs[0].Up
		}},
		{name: "nginx", metric: "nginx_connections_active 1\n", check: func(url string) bool {
			_, xs := NewNginxCollector("node", []model.NginxInstanceConfig{{Name: "x", Addr: "x:80", ExporterURL: url}}).CollectCtx(context.Background())
			return len(xs) == 1 && xs[0].Up
		}},
		{name: "redis", metric: "redis_connected_clients 1\n", check: func(url string) bool {
			_, xs := NewRedisCollector("node", []model.RedisInstanceConfig{{Name: "x", Addr: "x:6379", ExporterURL: url}}).CollectCtx(context.Background())
			return len(xs) == 1 && xs[0].Up
		}},
		{name: "mongodb", metric: "mongodb_connections_current 1\n", check: func(url string) bool {
			_, xs := NewMongoDBCollector("node", []model.MongoDBInstanceConfig{{Name: "x", Addr: "x:27017", ExporterURL: url}}).CollectCtx(context.Background())
			return len(xs) == 1 && xs[0].Up
		}},
		{name: "fastdfs", metric: "fastdfs_storage_count 1\n", check: func(url string) bool {
			_, xs := NewFastDFSCollector("node", []model.FastDFSInstanceConfig{{Name: "x", Addr: "x:22122", ExporterURL: url}}).CollectCtx(context.Background())
			return len(xs) == 1 && xs[0].Up
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.check(expositionServer(t, "").URL) {
				t.Fatal("empty exposition reported online")
			}
			if !tc.check(expositionServer(t, tc.metric).URL) {
				t.Fatal("expected metric did not report online")
			}
		})
	}
}

func TestK8sExporterCommentOnlyExpositionStaysOffline(t *testing.T) {
	body := "# HELP kube_node_status_condition node condition\n# TYPE kube_node_status_condition gauge\n# HELP kube_pod_status_phase pod phase\n"
	collector := NewK8sCollector("node-a", []model.K8sInstanceConfig{{Name: "cluster", APIServer: "https://127.0.0.1:6443", Token: "test", ExporterURL: expositionServer(t, body).URL, InsecureTLS: true}})
	result := collector.CollectCtx(context.Background())
	if len(result.Instances) != 1 || result.Instances[0].Up {
		t.Fatalf("comment-only KSM exposition reported online: %+v", result.Instances)
	}
}

func TestK8sExporterEmptyExpositionStaysOffline(t *testing.T) {
	for _, body := range []string{"", "# HELP kube_node_info info\n", "process_cpu_seconds_total 1\n"} {
		collector := NewK8sCollector("node-a", []model.K8sInstanceConfig{{Name: "cluster", APIServer: "https://127.0.0.1:6443", Token: "test", ExporterURL: expositionServer(t, body).URL, InsecureTLS: true}})
		result := collector.CollectCtx(context.Background())
		if len(result.Instances) != 1 || result.Instances[0].Up {
			t.Fatalf("empty KSM exposition reported online: %+v", result.Instances)
		}
		for _, metric := range result.Metrics {
			if metric.Name == "k8s_cluster_up" && metric.Value != 0 {
				t.Fatalf("empty KSM generated up=1: %+v", metric)
			}
		}
	}
}

// 被 metric-allowlist 收窄的健康 KSM 只暴露少量 kube_* 族，仍应判在线。
func TestK8sExporterNarrowAllowlistStaysOnline(t *testing.T) {
	body := "# HELP kube_namespace_created [ALPHA] info\nkube_namespace_created{namespace=\"default\"} 1\n"
	collector := NewK8sCollector("node-a", []model.K8sInstanceConfig{{Name: "cluster", APIServer: "https://127.0.0.1:6443", Token: "test", ExporterURL: expositionServer(t, body).URL, InsecureTLS: true}})
	result := collector.CollectCtx(context.Background())
	if len(result.Instances) != 1 || !result.Instances[0].Up {
		t.Fatalf("narrow-but-valid KSM exposition reported offline: %+v", result.Instances)
	}
}
