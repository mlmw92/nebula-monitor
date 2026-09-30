package api

import (
	"strings"
	"testing"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// clusterStore 是 storage.Storage 的测试替身：按指标名返回预置的即时查询序列。
type clusterStore struct{ instant map[string][]model.Series }

func (s *clusterStore) Write([]model.Metric) error { return nil }
func (s *clusterStore) QueryRange(string, string, map[string]string, int64, int64, int64) ([]model.Series, error) {
	return nil, nil
}
func (s *clusterStore) QueryLatest(string, string, map[string]string) (*model.Point, error) {
	return nil, nil
}
func (s *clusterStore) QueryInstant(_, name string, _ map[string]string) ([]model.Series, error) {
	return s.instant[name], nil
}
func (s *clusterStore) QueryInstantWithLookback(_, name string, _ map[string]string, _ time.Duration) ([]model.Series, error) {
	return s.instant[name], nil
}
func (s *clusterStore) QueryAllLatest(string, map[string]string) ([]model.Series, error) {
	return nil, nil
}
func (s *clusterStore) Close() error    { return nil }
func (s *clusterStore) Backend() string { return "fake" }

func grSeries(labels map[string]string, value float64) model.Series {
	return model.Series{Labels: labels, Points: []model.Point{{Timestamp: 1700000000000, Value: value}}}
}

// buildFaultCluster 构造一个 3 成员 GR 集群的时序数据：全部为 PRIMARY、视图一致，
// 只把「单主/多主模式」作为变量，用于验证接口返回的结论确实来自模式感知判定。
func buildFaultCluster(modeValue float64) *clusterStore {
	members := []string{"mysql-gr-1", "mysql-gr-2", "mysql-gr-3"}
	up := make([]model.Series, 0, len(members))
	modes := make([]model.Series, 0, len(members))
	views := make([]model.Series, 0, len(members)*len(members))
	for i, host := range members {
		inst := "10.0.0.10:330" + string(rune('7'+i))
		base := map[string]string{
			"node": "node-1", "instance": inst, "group": "dev-mysql-gr",
			"name": "dev-mysql-gr", "topology": "cluster", "role": "primary",
		}
		up = append(up, grSeries(base, 1))
		modes = append(modes, grSeries(map[string]string{"node": "node-1", "instance": inst, "group": "dev-mysql-gr"}, modeValue))
		for _, m := range members {
			views = append(views, grSeries(map[string]string{
				"node": "node-1", "instance": inst, "group": "dev-mysql-gr", "member": m,
			}, 1))
			_ = host
		}
	}
	return &clusterStore{instant: map[string][]model.Series{
		"mysql_instance_up":            up,
		"mysql_gr_single_primary_mode": modes,
		"mysql_gr_view_member":         views,
	}}
}

func visibleMySQLInstances() []mysqlInstanceInfo {
	out := make([]mysqlInstanceInfo, 0, 3)
	for i, inst := range []string{"10.0.0.10:3307", "10.0.0.10:3308", "10.0.0.10:3309"} {
		_ = i
		out = append(out, mysqlInstanceInfo{
			Node: "node-1", Instance: inst, Name: "dev-mysql-gr",
			Group: "dev-mysql-gr", Topology: "cluster", Role: "primary", Up: true,
		})
	}
	return out
}

// 单主模式下三个实例都是 PRIMARY：接口必须给出「多主（疑似脑裂）」结论，
// 与「MySQL 集群状态损坏」告警规则同一口径（此前页面自己算，会显示"运行正常"）。
func TestBuildMySQLClusters_SinglePrimaryModeReportsMultiMasterFault(t *testing.T) {
	a := scopeTestAPI(t)
	a.store = buildFaultCluster(1) // 1 = 单主模式

	clusters := a.buildMySQLClusters(visibleMySQLInstances())
	if len(clusters) != 1 {
		t.Fatalf("应返回 1 个集群组，实际 %+v", clusters)
	}
	c := clusters[0]
	if c.Mode != "single" {
		t.Errorf("模式应为 single，实际 %q", c.Mode)
	}
	if !strings.Contains(c.Fault, "多主") {
		t.Fatalf("单主模式下 3 主应判「多主」异常，实际 fault=%q", c.Fault)
	}
}

// 多主模式（single_primary_mode=OFF）下全部成员都是主库属正常形态：不得报异常。
func TestBuildMySQLClusters_MultiPrimaryModeIsHealthy(t *testing.T) {
	a := scopeTestAPI(t)
	a.store = buildFaultCluster(0) // 0 = 多主模式

	clusters := a.buildMySQLClusters(visibleMySQLInstances())
	if len(clusters) != 1 {
		t.Fatalf("应返回 1 个集群组，实际 %+v", clusters)
	}
	if clusters[0].Mode != "multi" {
		t.Errorf("模式应为 multi，实际 %q", clusters[0].Mode)
	}
	if clusters[0].Fault != "" {
		t.Fatalf("多主模式全主应判健康，实际 fault=%q", clusters[0].Fault)
	}
}

// 组视图不一致（各节点只看到自己）：即使模式是多主也必须判异常。
func TestBuildMySQLClusters_ViewSplitIsReported(t *testing.T) {
	store := buildFaultCluster(0)
	// 把视图改成「每个实例只看到自己」——各自为组的典型形态。
	var split []model.Series
	for _, s := range store.instant["mysql_gr_view_member"] {
		self := s.Labels["instance"]
		host := map[string]string{"10.0.0.10:3307": "mysql-gr-1", "10.0.0.10:3308": "mysql-gr-2", "10.0.0.10:3309": "mysql-gr-3"}[self]
		labels := map[string]string{"node": "node-1", "instance": self, "group": "dev-mysql-gr", "member": host}
		split = append(split, grSeries(labels, 1))
	}
	store.instant["mysql_gr_view_member"] = split

	a := scopeTestAPI(t)
	a.store = store
	clusters := a.buildMySQLClusters(visibleMySQLInstances())
	if len(clusters) != 1 || !strings.Contains(clusters[0].Fault, "组视图分裂") {
		t.Fatalf("视图不一致应判「组视图分裂」，实际 %+v", clusters)
	}
}

// 非集群拓扑（standalone/replication）不参与集群判定。
func TestBuildMySQLClusters_IgnoresNonClusterTopology(t *testing.T) {
	a := scopeTestAPI(t)
	a.store = buildFaultCluster(0)
	if got := a.buildMySQLClusters([]mysqlInstanceInfo{{
		Node: "node-1", Instance: "10.0.0.10:3306", Group: "dev-mysql", Topology: "standalone", Up: true,
	}}); len(got) != 0 {
		t.Fatalf("非集群拓扑不应产出集群结论，实际 %+v", got)
	}
}
