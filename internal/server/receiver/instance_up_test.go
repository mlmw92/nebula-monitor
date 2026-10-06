package receiver

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/config"
	"github.com/nebula/monitor/internal/server/node"
)

// recordingStorage 记录写入的指标，供断言"存活序列到底有没有产出"。
// 与 assets_test.go 的 assetTestStorage 的区别：那个只数次数，这个要读内容。
type recordingStorage struct{ metrics []model.Metric }

func (s *recordingStorage) Write(ms []model.Metric) error {
	s.metrics = append(s.metrics, ms...)
	return nil
}
func (s *recordingStorage) QueryRange(string, string, map[string]string, int64, int64, int64) ([]model.Series, error) {
	return nil, nil
}
func (s *recordingStorage) QueryLatest(string, string, map[string]string) (*model.Point, error) {
	return nil, nil
}
func (s *recordingStorage) QueryInstant(string, string, map[string]string) ([]model.Series, error) {
	return nil, nil
}
func (s *recordingStorage) QueryInstantWithLookback(string, string, map[string]string, time.Duration) ([]model.Series, error) {
	return nil, nil
}
func (s *recordingStorage) QueryAllLatest(string, map[string]string) ([]model.Series, error) {
	return nil, nil
}
func (s *recordingStorage) Close() error    { return nil }
func (s *recordingStorage) Backend() string { return "test" }

// upSeries 找出某个实例的存活序列（返回命中条数与值）。
func upSeries(metrics []model.Metric, name, instance string) (int, float64) {
	n, value := 0, -1.0
	for _, m := range metrics {
		if m.Name != name || m.Labels["instance"] != instance {
			continue
		}
		n++
		value = m.Value
	}
	return n, value
}

// exporter 模式下 6 类中间件缺 <mw>_instance_up，导致「中间件离线」告警永不触发。
// 这些序列必须由 receiver 从实例元信息补出来，且离线实例要如实写 0。
func TestHandleReportSynthesizesInstanceUpForExporterMode(t *testing.T) {
	dir := t.TempDir()
	store := &recordingStorage{}
	mgr := node.New(filepath.Join(dir, "nodes.json"), time.Minute)
	rec := New(store, mgr, config.AgentAuthConfig{}, nil, nil, nil, nil)

	postReport(t, rec, model.ReportPayload{
		Node: "web-01", Group: "g1",
		MySQLInstances: []model.MySQLInstance{{
			Instance: "db:3306", Name: "mysql-main", Group: "core", Role: "master", Topology: "standalone", Up: false,
		}},
		PostgresInstances: []model.PostgresInstance{{
			Instance: "pg:5432", Name: "pg-main", Group: "core", Role: "master", Up: true,
		}},
		NginxInstances:  []model.NginxInstance{{Instance: "ng:80", Name: "ng-main", Group: "core", Up: true}},
		KafkaInstances:  []model.KafkaInstance{{Instance: "kafka:9092", Name: "kafka-main", Group: "core", Role: "broker", Up: false}},
		MongoDBInstances: []model.MongoDBInstance{{
			Instance: "mongo:27017", Name: "mongo-main", Group: "core", Role: "PRIMARY", Topology: "replicaset", Up: true,
		}},
		FastDFSInstances: []model.FastDFSInstance{{Instance: "fdfs:22122", Name: "fdfs-main", Group: "core", Role: "tracker", Up: false}},
	})

	for _, tc := range []struct {
		name, instance string
		wantValue      float64
	}{
		{"mysql_instance_up", "db:3306", 0},
		{"postgres_instance_up", "pg:5432", 1},
		{"nginx_instance_up", "ng:80", 1},
		{"kafka_instance_up", "kafka:9092", 0},
		{"mongodb_instance_up", "mongo:27017", 1},
		{"fastdfs_instance_up", "fdfs:22122", 0},
	} {
		n, value := upSeries(store.metrics, tc.name, tc.instance)
		if n != 1 || value != tc.wantValue {
			t.Fatalf("%s{instance=%q} 应有且仅有 1 条、值为 %v，实际 %d 条 value=%v",
				tc.name, tc.instance, tc.wantValue, n, value)
		}
	}
}

// 直连模式：采集器自己产出同名序列，receiver 必须**不重复写**。
// 重复会形成同一实例的两条序列（label 集不同），PromQL 取最新会来回摇摆。
func TestHandleReportDoesNotDuplicateCollectorInstanceUp(t *testing.T) {
	dir := t.TempDir()
	store := &recordingStorage{}
	mgr := node.New(filepath.Join(dir, "nodes.json"), time.Minute)
	rec := New(store, mgr, config.AgentAuthConfig{}, nil, nil, nil, nil)

	postReport(t, rec, model.ReportPayload{
		Node: "web-01", Group: "g1",
		// 直连采集器已经产出的那一条（带自己的标签集）
		Metrics: []model.Metric{{
			Node: "web-01", Name: "mysql_instance_up", Timestamp: 1000,
			Labels: map[string]string{"instance": "db:3306", "name": "mysql-main", "role": "master"},
			Value:  1,
		}},
		MySQLInstances: []model.MySQLInstance{{
			Instance: "db:3306", Name: "mysql-main", Role: "master", Up: true,
		}},
	})

	n, value := upSeries(store.metrics, "mysql_instance_up", "db:3306")
	if n != 1 || value != 1 {
		t.Fatalf("已有采集器序列时不得再补一条：实际 %d 条 value=%v", n, value)
	}
}

// 同一台机器上的多个同类型实例：判重按「指标名 + instance」，
// 只按名字判重会让第二个实例永久缺一条序列。
func TestHandleReportSynthesizesPerInstance(t *testing.T) {
	dir := t.TempDir()
	store := &recordingStorage{}
	mgr := node.New(filepath.Join(dir, "nodes.json"), time.Minute)
	rec := New(store, mgr, config.AgentAuthConfig{}, nil, nil, nil, nil)

	postReport(t, rec, model.ReportPayload{
		Node: "web-01", Group: "g1",
		MySQLInstances: []model.MySQLInstance{
			{Instance: "db-a:3306", Name: "a", Up: true},
			{Instance: "db-b:3306", Name: "b", Up: false},
		},
	})

	if n, v := upSeries(store.metrics, "mysql_instance_up", "db-a:3306"); n != 1 || v != 1 {
		t.Fatalf("实例 a 应有 1 条 up=1，实际 %d 条 value=%v", n, v)
	}
	if n, v := upSeries(store.metrics, "mysql_instance_up", "db-b:3306"); n != 1 || v != 0 {
		t.Fatalf("实例 b 应有 1 条 up=0，实际 %d 条 value=%v", n, v)
	}
}
