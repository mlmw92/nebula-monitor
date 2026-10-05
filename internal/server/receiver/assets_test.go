package receiver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/asset"
	"github.com/nebula/monitor/internal/server/config"
	"github.com/nebula/monitor/internal/server/node"
)

// assetTestStorage 是 storage.Storage 的最小替身：只记录写入，其余方法返回空。
// 本测试关心的是「上报是否写入资产台账」，时序库写入只是必须存在的一环。
type assetTestStorage struct{ writes int }

func (s *assetTestStorage) Write([]model.Metric) error { s.writes++; return nil }
func (s *assetTestStorage) QueryRange(string, string, map[string]string, int64, int64, int64) ([]model.Series, error) {
	return nil, nil
}
func (s *assetTestStorage) QueryLatest(string, string, map[string]string) (*model.Point, error) {
	return nil, nil
}
func (s *assetTestStorage) QueryInstant(string, string, map[string]string) ([]model.Series, error) {
	return nil, nil
}
func (s *assetTestStorage) QueryInstantWithLookback(string, string, map[string]string, time.Duration) ([]model.Series, error) {
	return nil, nil
}
func (s *assetTestStorage) QueryAllLatest(string, map[string]string) ([]model.Series, error) {
	return nil, nil
}
func (s *assetTestStorage) Close() error    { return nil }
func (s *assetTestStorage) Backend() string { return "test" }

func postReport(t *testing.T, r *Receiver, payload model.ReportPayload) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("序列化上报失败: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/report", bytes.NewReader(body))
	w := httptest.NewRecorder()
	r.HandleReport(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("上报状态码 = %d，响应 %s", w.Code, w.Body.String())
	}
}

// 上报应把主机与中间件实例写入资产台账，并建立「实例 runs_on 主机」关系；
// 重复上报不能产生重复资产或多余的变更记录（幂等）。
func TestHandleReportWritesAssetLedger(t *testing.T) {
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

	payload := model.ReportPayload{
		Node: "web-01", Group: "g1", OS: "Ubuntu 24.04", Arch: "amd64", IP: "10.0.0.5", Version: "1.29.0",
		HostInfo:       model.HostInfo{CPUModel: "EPYC 7K62", CPUCores: 4, MemoryTotal: 8 << 30, DiskTotal: 100 << 30},
		RedisInstances: []model.RedisInstance{{Instance: "127.0.0.1:6379", Name: "dev-redis", Group: "dev", Role: "master", Up: true}},
		K8sInstances:   []model.K8sInstance{{Instance: "https://127.0.0.1:6443", Name: "dev-k8s", Version: "v1.30", Up: true}},
		// 已退出的容器：up=false，但容器自身的 status/image 必须落到台账——
		// 否则列表里只能看到"离线"，说不清是退出了还是没起来（真实反馈）。
		DockerInstances: []model.DockerInstance{{
			Instance: "abc123def456", Name: "mw-es", Group: "default",
			Image: "elasticsearch:8.13", Status: "exited", Up: false,
		}},
	}
	postReport(t, rec, payload)
	postReport(t, rec, payload) // 第二轮：验证幂等

	host, ok, err := svc.Get(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "web-01"})
	if err != nil {
		t.Fatalf("查询主机资产失败: %v", err)
	}
	if !ok {
		t.Fatal("主机资产未写入")
	}
	if v, _ := host.Value("os"); v != "Ubuntu 24.04" {
		t.Fatalf("主机 os 属性不符: %q", v)
	}
	// 容量按 MB 归一：8 GiB 应为 8192 MB（避免台账里混入字节量纲）
	if v, _ := host.Value("memoryMB"); v != "8192" {
		t.Fatalf("主机 memoryMB 应为 8192，实际 %q", v)
	}
	if v, _ := host.Value("agentVersion"); v != "1.29.0" {
		t.Fatalf("主机 agentVersion 属性不符: %q", v)
	}

	instances, err := svc.List(asset.ListFilter{TypeKey: asset.TypeMiddlewareInst})
	if err != nil {
		t.Fatalf("查询实例资产失败: %v", err)
	}
	if len(instances) != 3 {
		t.Fatalf("实例资产应为 3 个（redis + kubernetes + docker），实际 %d", len(instances))
	}

	// 容器的运行状态与镜像要能读到：台账靠它们回答"为什么不可达"
	container, ok, err := svc.Get(asset.Ref{TypeKey: asset.TypeMiddlewareInst, NaturalKey: "docker:abc123def456"})
	if err != nil || !ok {
		t.Fatalf("查询容器资产失败: ok=%v err=%v", ok, err)
	}
	if v, _ := container.ValueFrom("status", asset.SourceDiscovery); v != "exited" {
		t.Fatalf("容器 status 应为 exited，实际 %q", v)
	}
	if v, _ := container.ValueFrom("image", asset.SourceDiscovery); v != "elasticsearch:8.13" {
		t.Fatalf("容器 image 应落到台账，实际 %q", v)
	}
	if v, _ := container.ValueFrom("up", asset.SourceDiscovery); v != "false" {
		t.Fatalf("已退出容器 up 应为 false，实际 %q", v)
	}

	redisRef := asset.Ref{TypeKey: asset.TypeMiddlewareInst, NaturalKey: "redis:127.0.0.1:6379"}
	links, err := svc.Links(redisRef)
	if err != nil {
		t.Fatalf("查询资产关联失败: %v", err)
	}
	if len(links) != 1 || links[0].Kind != asset.LinkRunsOn || links[0].To.NaturalKey != "web-01" {
		t.Fatalf("实例应关联到主机（runs_on web-01），实际 %+v", links)
	}

	history, err := svc.History(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "web-01"}, 0)
	if err != nil {
		t.Fatalf("查询变更历史失败: %v", err)
	}
	if len(history) != 1 || history[0].Kind != asset.ChangeInitial {
		t.Fatalf("重复上报后主机应只有一条建档记录，实际 %+v", history)
	}
}

// 未注入资产服务时上报照常成功（台账是可选能力，不改变既有行为）。
func TestHandleReportWithoutAssetServiceStillSucceeds(t *testing.T) {
	dir := t.TempDir()
	mgr := node.New(filepath.Join(dir, "nodes.json"), time.Minute)
	rec := New(&assetTestStorage{}, mgr, config.AgentAuthConfig{}, nil, nil, nil, nil)
	postReport(t, rec, model.ReportPayload{Node: "web-02", Group: "g1"})
}

// K8s 清单落台账：工作负载先落（它是 member_of 的对端），Pod 落完再建两条边。
//
// 这里刻意放一个"落在未注册节点上的 Pod"：它必须照样落台账，但归属节点落空串
// （仅全局范围可见）且**不建 runs_on**——不假装它属于某个分组，也不为它编一个主机资产。
func TestHandleReportWritesContainerInventory(t *testing.T) {
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
		K8sWorkloads: []model.K8sWorkload{{
			Cluster: cluster, Namespace: "default", Kind: "deployment", Name: "web",
			Desired: 3, Ready: 2, Image: "nginx:1.27",
		}},
		K8sPods: []model.K8sPod{
			{Cluster: cluster, Namespace: "default", Name: "web-7d9f-abc", Node: "web-01",
				Phase: "Running", Status: "Running", Ready: 1, Total: 1,
				OwnerKind: "deployment", OwnerName: "web", Image: "nginx:1.27"},
			{Cluster: cluster, Namespace: "default", Name: "web-7d9f-def", Node: "worker-99",
				Phase: "Running", Status: "Running", Ready: 1, Total: 1},
		},
	}
	postReport(t, rec, payload)
	postReport(t, rec, payload) // 第二轮：验证幂等（不应多出资产或边）

	// 工作负载：跨节点、没有单一归属节点 → 范围挂在集群的上报主机上
	wlRef := asset.Ref{
		TypeKey:    asset.TypeWorkload,
		NaturalKey: asset.WorkloadNaturalKey(cluster, "default", "deployment", "web"),
	}
	wl, ok, err := svc.Get(wlRef)
	if err != nil || !ok {
		t.Fatalf("工作负载资产未写入: ok=%v err=%v", ok, err)
	}
	if wl.Node != "web-01" {
		t.Fatalf("工作负载的归属节点应为集群上报主机 web-01，实际 %q", wl.Node)
	}
	if v, _ := wl.ValueFrom("desired", asset.SourceDiscovery); v != "3" {
		t.Fatalf("工作负载 desired 应为 3，实际 %q", v)
	}

	// 落在已注册节点上的 Pod：归属该节点，两条边都建得出来
	podRef := asset.Ref{TypeKey: asset.TypePod, NaturalKey: asset.PodNaturalKey(cluster, "default", "web-7d9f-abc")}
	pod, ok, err := svc.Get(podRef)
	if err != nil || !ok {
		t.Fatalf("容器资产未写入: ok=%v err=%v", ok, err)
	}
	if pod.Node != "web-01" {
		t.Fatalf("容器应归属它实际所在的节点（web-01），实际 %q", pod.Node)
	}
	links, err := svc.Links(podRef)
	if err != nil {
		t.Fatalf("查询容器关联失败: %v", err)
	}
	peers := map[asset.LinkKind]string{}
	for _, l := range links {
		peers[l.Kind] = l.To.NaturalKey
		if l.Source != asset.SourceDiscovery {
			t.Fatalf("自动建立的边来源应为 discovery，实际 %q", l.Source)
		}
	}
	if len(links) != 2 || peers[asset.LinkRunsOn] != "web-01" || peers[asset.LinkMemberOf] != wlRef.NaturalKey {
		t.Fatalf("容器应同时 runs_on 主机与 member_of 工作负载，实际 %+v", links)
	}

	// 落在未注册节点上的 Pod：照样落台账，但归属节点为空、且没有 runs_on
	orphanRef := asset.Ref{TypeKey: asset.TypePod, NaturalKey: asset.PodNaturalKey(cluster, "default", "web-7d9f-def")}
	orphan, ok, err := svc.Get(orphanRef)
	if err != nil || !ok {
		t.Fatalf("未注册节点上的容器也应落台账: ok=%v err=%v", ok, err)
	}
	if orphan.Node != "" {
		t.Fatalf("节点没有主机资产时应落空串（仅全局可见），实际 %q", orphan.Node)
	}
	if v, _ := orphan.ValueFrom("k8sNode", asset.SourceDiscovery); v != "worker-99" {
		t.Fatalf("原始 spec.nodeName 应仍作为属性保留（用于展示），实际 %q", v)
	}
	orphanLinks, err := svc.Links(orphanRef)
	if err != nil {
		t.Fatalf("查询容器关联失败: %v", err)
	}
	if len(orphanLinks) != 0 {
		t.Fatalf("未注册节点的容器不应有 runs_on（对端不存在），实际 %+v", orphanLinks)
	}
}

// K8s 节点名与 Agent 上报的 hostname **大小写不一致**时，必须仍归到同一台主机。
//
// 真实情形（2026-10-05 在 dev-server 上实测到的）：k3s 的节点名按 RFC 1123 一律小写
// （vm-0-10-ubuntu），而云主机上报的 hostname 保留系统原样（VM-0-10-ubuntu）。
// 按原文严格比对会把**同一台机器**判成两台，于是：① Pod 的 runs_on 建不出来；
// ② 它的归属节点为空 —— **按节点分组授权的受限用户看不到自己机器上的 Pod**。
// 两处失效都不报错，只表现为"看起来没有关系"，所以这条必须有用例钉住。
func TestHandleReportFoldsHostNameCase(t *testing.T) {
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

	const cluster = "https://127.0.0.1:6443"
	// Agent 上报的 hostname 是大写；K8s 给的 spec.nodeName 是小写
	postReport(t, rec, model.ReportPayload{
		Node:         "VM-0-10-ubuntu",
		K8sInstances: []model.K8sInstance{{Instance: cluster, Name: "k3s-dev", Up: true}},
		K8sWorkloads: []model.K8sWorkload{{
			Cluster: cluster, Namespace: "nebula-demo", Kind: "deployment", Name: "web", Desired: 1, Ready: 1,
		}},
		K8sPods: []model.K8sPod{{
			Cluster: cluster, Namespace: "nebula-demo", Name: "web-abc", Node: "vm-0-10-ubuntu",
			Phase: "Running", Status: "Running", Ready: 1, Total: 1,
			OwnerKind: "deployment", OwnerName: "web",
		}},
	})

	podRef := asset.Ref{TypeKey: asset.TypePod, NaturalKey: asset.PodNaturalKey(cluster, "nebula-demo", "web-abc")}
	pod, ok, err := svc.Get(podRef)
	if err != nil || !ok {
		t.Fatalf("容器资产未写入: ok=%v err=%v", ok, err)
	}
	// 归属节点必须是**台账里的规范键**：只有规范写法才能在资源范围里查到节点分组
	if pod.Node != "VM-0-10-ubuntu" {
		t.Fatalf("归属节点应折叠大小写并取台账规范键 VM-0-10-ubuntu，实际 %q", pod.Node)
	}
	// 原文仍作为属性保留（展示用，说明它实际跑在哪个 k8s 节点上）
	if v, _ := pod.ValueFrom("k8sNode", asset.SourceDiscovery); v != "vm-0-10-ubuntu" {
		t.Fatalf("k8sNode 属性应保留原文，实际 %q", v)
	}

	links, err := svc.Links(podRef)
	if err != nil {
		t.Fatalf("查询容器关联失败: %v", err)
	}
	peers := map[asset.LinkKind]string{}
	for _, l := range links {
		peers[l.Kind] = l.To.NaturalKey
	}
	if peers[asset.LinkRunsOn] != "VM-0-10-ubuntu" {
		t.Fatalf("runs_on 应指向大写的主机资产，实际 %+v", links)
	}
	if peers[asset.LinkMemberOf] == "" {
		t.Fatalf("member_of 也应同时存在，实际 %+v", links)
	}
}
