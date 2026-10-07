package receiver

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/asset"
	"github.com/nebula/monitor/internal/server/config"
	"github.com/nebula/monitor/internal/server/node"
)

// K8s Service 落台账并建 service → pod 边。
//
// 覆盖四个必须做对、且都不报错的点：
//  1. **归属节点取集群的上报主机**（Service 跨节点、没有单一归属节点；落空串会让受限用户完全看不到它）；
//  2. **只给本轮真的落过的 Pod 建边**：后端 Pod 缺席（被截断、落在被排除的命名空间）时跳过，
//     不刷日志、**也不造占位 Pod**；
//  3. **幂等**：同一份清单连报两轮，资产与边都不增加；
//  4. **无后端只落资产不建边**（不猜后端是谁）。
func TestHandleReportWritesServiceInventoryAndExposesEdges(t *testing.T) {
	dir := t.TempDir()
	store, err := asset.Open(filepath.Join(dir, "assets.db"))
	if err != nil {
		t.Fatalf("打开资产库失败: %v", err)
	}
	defer store.Close()
	svc := asset.NewService(store)

	mgr := node.New(filepath.Join(dir, "nodes.json"), time.Minute)
	rec := New(&assetTestStorage{}, mgr, config.AgentAuthConfig{}, nil, nil, nil, nil)
	rec.SetAssetService(svc)

	const cluster = "https://10.0.0.9:6443"
	payload := model.ReportPayload{
		Node:         "web-01",
		Group:        "g1",
		K8sInstances: []model.K8sInstance{{Instance: cluster, Name: "dev-k8s", Up: true}},
		K8sPods: []model.K8sPod{
			// 本轮真的落过的 Pod：service 的后端
			{Cluster: cluster, Namespace: "default", Name: "web-abc", Node: "web-01",
				Phase: "Running", Status: "Running", Ready: 1, Total: 1},
		},
		K8sServices: []model.K8sService{
			{
				Cluster: cluster, Namespace: "default", Name: "web",
				Type: "ClusterIP", ClusterIP: "10.43.0.10",
				// 三个后端：第一个本轮落过 → 建边；第二个不在本轮清单里 → 跳过且不造占位；
				// 第三个虽然名字对得上，但它是别的命名空间的（PodNaturalKey 带命名空间，自然落空）。
				BackendPods: []string{"web-abc", "gone-xyz", "web-abc"},
			},
			// 无后端（选择器无匹配 / ExternalName）：只落资产
			{Cluster: cluster, Namespace: "default", Name: "empty-svc", Type: "ExternalName"},
		},
	}
	postReport(t, rec, payload)
	postReport(t, rec, payload) // 第二轮：验证幂等（不应多出资产或边）

	// 1) Service 资产与归属节点
	svcRef := asset.Ref{TypeKey: asset.TypeService, NaturalKey: asset.ServiceNaturalKey(cluster, "default", "web")}
	got, ok, err := svc.Get(svcRef)
	if err != nil || !ok {
		t.Fatalf("Service 资产未写入: ok=%v err=%v", ok, err)
	}
	if got.Node != "web-01" {
		t.Fatalf("Service 的归属节点应为集群上报主机 web-01，实际 %q", got.Node)
	}
	if v, _ := got.ValueFrom("type", asset.SourceDiscovery); v != "ClusterIP" {
		t.Fatalf("type 属性应为 ClusterIP，实际 %q", v)
	}
	if v, _ := got.ValueFrom("clusterIP", asset.SourceDiscovery); v != "10.43.0.10" {
		t.Fatalf("clusterIP 属性应为 10.43.0.10，实际 %q", v)
	}
	if v, _ := got.ValueFrom("backends", asset.SourceDiscovery); v != "3" {
		t.Fatalf("backends 属性应记上报的后端数 3，实际 %q", v)
	}

	// 2) 边：只对"本轮真的落过的 Pod"建，且幂等
	links, err := svc.Links(svcRef)
	if err != nil {
		t.Fatalf("查询 Service 关联失败: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("两轮上报后应只有 1 条边（幂等），实际 %d 条：%+v", len(links), links)
	}
	if links[0].Kind != asset.LinkExposes {
		t.Fatalf("关系种类应为 exposes，实际 %q", links[0].Kind)
	}
	if links[0].Source != asset.SourceDiscovery {
		t.Fatalf("自动建立的边来源应为 discovery，实际 %q", links[0].Source)
	}
	if want := asset.PodNaturalKey(cluster, "default", "web-abc"); links[0].To.NaturalKey != want {
		t.Fatalf("边的对端应是 web-abc，实际 %q", links[0].To.NaturalKey)
	}

	// 3) 缺席的后端**不造占位 Pod**：它只是没有边，而不是凭空多一个资产
	ghostRef := asset.Ref{TypeKey: asset.TypePod, NaturalKey: asset.PodNaturalKey(cluster, "default", "gone-xyz")}
	if _, ok, err := svc.Get(ghostRef); err != nil || ok {
		t.Fatalf("缺席的后端不该被造成资产: ok=%v err=%v", ok, err)
	}

	// 4) 无后端的 Service：资产在、边没有
	emptyRef := asset.Ref{TypeKey: asset.TypeService, NaturalKey: asset.ServiceNaturalKey(cluster, "default", "empty-svc")}
	if _, ok, err := svc.Get(emptyRef); err != nil || !ok {
		t.Fatalf("无后端的 Service 也应落资产: ok=%v err=%v", ok, err)
	}
	if emptyLinks, err := svc.Links(emptyRef); err != nil || len(emptyLinks) != 0 {
		t.Fatalf("无后端的 Service 不该有边: err=%v links=%+v", err, emptyLinks)
	}
}

// Service 是稳定对象，**不进** EphemeralTypes——它停止上报就是真失联，应当计入台账与健康度。
// 这条与 Pod/Workload 的取舍正好相反，是刻意的，所以钉住它。
func TestServiceTypeIsNotEphemeral(t *testing.T) {
	for _, t2 := range asset.EphemeralTypes() {
		if t2 == asset.TypeService {
			t.Fatalf("Service 是稳定对象，不应被当作短命对象排除在台账与健康度之外")
		}
	}
	// 反向确认这两个仍在（否则上面那条会因为"列表被清空"而空转通过）
	got := map[string]bool{}
	for _, t2 := range asset.EphemeralTypes() {
		got[t2] = true
	}
	if !got[asset.TypePod] || !got[asset.TypeWorkload] {
		t.Fatalf("Pod 与 Workload 仍应是短命对象，实际 %v", got)
	}
}
