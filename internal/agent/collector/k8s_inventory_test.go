package collector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/nebula/monitor/internal/model"
)

// fakeK8sAPIServer 是一个只回清单相关路径的假 apiserver。
//
// 用真实 HTTP 而不是打桩采集器：本用例要验的恰恰是"从 apiserver 的 JSON 里**真的**取到了
// spec.nodeName 与 ownerReferences"——打桩会把这一步跳过去，而那正是最容易被写错的地方。
func fakeK8sAPIServer(t *testing.T, pods []any, deployments []any) *httptest.Server {
	t.Helper()
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	empty := func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, map[string]any{"items": []any{}}) }

	mux := http.NewServeMux()
	mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]string{"gitVersion": "v1.30.0"})
	})
	mux.HandleFunc("/api/v1/nodes", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"items": []any{}})
	})
	mux.HandleFunc("/api/v1/pods", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"items": pods})
	})
	mux.HandleFunc("/apis/apps/v1/deployments", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"items": deployments})
	})
	mux.HandleFunc("/apis/apps/v1/statefulsets", empty)
	mux.HandleFunc("/apis/apps/v1/daemonsets", empty)
	mux.HandleFunc("/apis/apps/v1/replicasets", func(w http.ResponseWriter, _ *http.Request) {
		// 一个由 Deployment "web" 拥有的 ReplicaSet——Pod 归属 Deployment 必须经这一跳。
		writeJSON(w, map[string]any{"items": []any{map[string]any{
			"metadata": map[string]any{
				"name": "web-7d9f", "namespace": "default",
				"ownerReferences": []any{map[string]any{
					"kind": "Deployment", "name": "web", "controller": true,
				}},
			},
		}}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func podJSON(namespace, name, node, image, phase string, ready bool, restarts int, owners []any) map[string]any {
	return map[string]any{
		"metadata": map[string]any{"name": name, "namespace": namespace, "ownerReferences": owners},
		"spec":     map[string]any{"nodeName": node, "containers": []any{map[string]any{"image": image}}},
		"status": map[string]any{
			"phase": phase, "startTime": "2026-10-04T10:00:00Z",
			"containerStatuses": []any{map[string]any{"name": "c", "ready": ready, "restartCount": restarts}},
		},
	}
}

func deploymentJSON(namespace, name, image string, desired, ready int) map[string]any {
	return map[string]any{
		"metadata": map[string]any{"name": name, "namespace": namespace},
		"spec": map[string]any{
			"replicas": desired,
			"template": map[string]any{"spec": map[string]any{
				"containers": []any{map[string]any{"image": image}},
			}},
		},
		"status": map[string]any{"readyReplicas": ready},
	}
}

func findPod(pods []model.K8sPod, name string) (model.K8sPod, bool) {
	for _, p := range pods {
		if p.Name == name {
			return p, true
		}
	}
	return model.K8sPod{}, false
}

// K8s 清单采集：把 apiserver 的 JSON 变成台账清单。
//
// 覆盖三个必须做对、且都容易写错的点：
//  1. **spec.nodeName 真的被读出来了**（此前根本没读——Pod 落台账与 runs_on 都要靠它）；
//  2. **Pod 经 ReplicaSet 解析到 Deployment**（Pod 的直接属主不是 Deployment）；
//  3. **系统命名空间默认不进清单**，但指标口径不受影响。
func TestK8sCollectorBuildsPodAndWorkloadInventory(t *testing.T) {
	srv := fakeK8sAPIServer(t,
		[]any{
			podJSON("default", "web-7d9f-abc", "worker-01", "nginx:1.27", "Running", true, 3,
				[]any{map[string]any{"kind": "ReplicaSet", "name": "web-7d9f", "controller": true}}),
			// 系统命名空间：默认不进清单
			podJSON("kube-system", "coredns-xyz", "worker-01", "coredns:1.11", "Running", true, 0, nil),
			// Job 建的 Pod：本批不给 Job 建工作负载资产，属主如实留空
			podJSON("default", "migrate-abc", "worker-01", "migrate:1", "Succeeded", false, 0,
				[]any{map[string]any{"kind": "Job", "name": "migrate", "controller": true}}),
		},
		[]any{
			deploymentJSON("default", "web", "nginx:1.27", 3, 2),
			deploymentJSON("kube-system", "coredns", "coredns:1.11", 2, 2),
		},
	)

	c := NewK8sCollector("worker-01", []model.K8sInstanceConfig{{
		Name: "dev-k8s", APIServer: srv.URL, Token: "test-token",
	}})
	res := c.CollectCtx(context.Background())

	if res.PodsTruncated || res.WorkloadsTruncated {
		t.Fatalf("小集群不应触发截断：%+v", res)
	}
	if len(res.Pods) != 2 {
		t.Fatalf("系统命名空间应被排除，期望 2 个 Pod，实际 %d：%+v", len(res.Pods), res.Pods)
	}
	if len(res.Workloads) != 1 || res.Workloads[0].Name != "web" {
		t.Fatalf("系统命名空间的工作负载也应被排除，实际 %+v", res.Workloads)
	}
	if res.Workloads[0].Desired != 3 || res.Workloads[0].Ready != 2 || res.Workloads[0].Image != "nginx:1.27" {
		t.Fatalf("工作负载清单字段不符：%+v", res.Workloads[0])
	}

	// 1）nodeName 必须被读出来
	pod, ok := findPod(res.Pods, "web-7d9f-abc")
	if !ok {
		t.Fatalf("未采到 Pod web-7d9f-abc：%+v", res.Pods)
	}
	if pod.Node != "worker-01" {
		t.Fatalf("spec.nodeName 应被读出为 worker-01，实际 %q", pod.Node)
	}
	if pod.Cluster != srv.URL {
		t.Fatalf("Cluster 应为 apiserver 地址 %q，实际 %q", srv.URL, pod.Cluster)
	}
	if pod.Ready != 1 || pod.Total != 1 || pod.Restarts != 3 {
		t.Fatalf("容器就绪/总数/重启数不符：%+v", pod)
	}
	if pod.Status != "Running" || pod.Phase != "Running" {
		t.Fatalf("phase 与有效状态不符：%+v", pod)
	}
	if pod.Image != "nginx:1.27" {
		t.Fatalf("镜像应为 nginx:1.27，实际 %q", pod.Image)
	}
	if pod.StartedAt == 0 {
		t.Fatalf("startTime 应被解析成毫秒时间戳：%+v", pod)
	}

	// 2）ReplicaSet → Deployment 这一跳
	if pod.OwnerKind != "deployment" || pod.OwnerName != "web" {
		t.Fatalf("Pod 应经 ReplicaSet 归属到 deployment/web，实际 %s/%s", pod.OwnerKind, pod.OwnerName)
	}

	// 3）Job 建的 Pod：属主如实留空，而不是塞给某个工作负载
	jobPod, ok := findPod(res.Pods, "migrate-abc")
	if !ok {
		t.Fatalf("未采到 Pod migrate-abc：%+v", res.Pods)
	}
	if jobPod.OwnerKind != "" || jobPod.OwnerName != "" {
		t.Fatalf("Job 建的 Pod 不应有工作负载属主，实际 %s/%s", jobPod.OwnerKind, jobPod.OwnerName)
	}

	// 指标口径不受清单排除影响：kube-system 的 Pod 仍要计入 k8s_pods_total
	var total float64
	for _, m := range res.Metrics {
		if m.Name == "k8s_pods_total" {
			total = m.Value
		}
	}
	if total != 3 {
		t.Fatalf("指标应统计全部 Pod（含系统命名空间）3 个，实际 %v", total)
	}
}

// 单轮上限：超限时**截断并置位**，而不是静默丢弃。
//
// 静默丢弃会让中心把"1000 个 Pod"当成全部——那种错误没有任何地方能暴露出来。
func TestK8sCollectorTruncatesInventoryAtReportLimit(t *testing.T) {
	pods := make([]any, 0, model.K8sPodsMaxPerReport+1)
	for i := 0; i <= model.K8sPodsMaxPerReport; i++ {
		pods = append(pods, podJSON("default", "pod-"+strconv.Itoa(i), "worker-01", "img:1", "Running", true, 0, nil))
	}
	srv := fakeK8sAPIServer(t, pods, nil)

	c := NewK8sCollector("worker-01", []model.K8sInstanceConfig{{
		Name: "big-k8s", APIServer: srv.URL, Token: "test-token",
	}})
	res := c.CollectCtx(context.Background())

	if len(res.Pods) != model.K8sPodsMaxPerReport {
		t.Fatalf("应截断到 %d 个，实际 %d", model.K8sPodsMaxPerReport, len(res.Pods))
	}
	if !res.PodsTruncated {
		t.Fatal("截断必须显式置位，否则中心会把 1000 个当成全部")
	}
}

// Pod 的直接属主是 ReplicaSet，不是 Deployment：必须经 rsOwners 解析，
// 否则最常见的 Deployment 场景下 member_of 一条都建不出来。
func TestPodOwnerResolvesReplicaSetToDeployment(t *testing.T) {
	rsPod := k8sObjectMeta{
		Namespace:       "default",
		OwnerReferences: []k8sOwnerReference{{Kind: "ReplicaSet", Name: "web-7d9f", Controller: true}},
	}
	if kind, name := podOwner(rsPod, map[string]string{"default/web-7d9f": "web"}); kind != "deployment" || name != "web" {
		t.Fatalf("应解析到 deployment/web，实际 %s/%s", kind, name)
	}
	// 映射缺失（ReplicaSet 列表读失败）时**不猜**：宁可这条边不存在，也不要按名字前缀编一个属主
	if kind, name := podOwner(rsPod, nil); kind != "" || name != "" {
		t.Fatalf("无法解析时应返回空而不是猜，实际 %s/%s", kind, name)
	}

	// StatefulSet / DaemonSet 的 Pod 直接被它们拥有，不需要额外一跳
	if kind, name := podOwner(k8sObjectMeta{OwnerReferences: []k8sOwnerReference{
		{Kind: "StatefulSet", Name: "db", Controller: true},
	}}, nil); kind != "statefulset" || name != "db" {
		t.Fatalf("StatefulSet 应直接归属，实际 %s/%s", kind, name)
	}
	if kind, name := podOwner(k8sObjectMeta{OwnerReferences: []k8sOwnerReference{
		{Kind: "DaemonSet", Name: "node-agent", Controller: true},
	}}, nil); kind != "daemonset" || name != "node-agent" {
		t.Fatalf("DaemonSet 应直接归属，实际 %s/%s", kind, name)
	}

	// Job 不在本批的工作负载范围内；非控制器的"引用"根本不是属主
	if kind, _ := podOwner(k8sObjectMeta{OwnerReferences: []k8sOwnerReference{
		{Kind: "Job", Name: "migrate", Controller: true},
	}}, nil); kind != "" {
		t.Fatalf("Job 不应被当成工作负载，实际 %q", kind)
	}
	if kind, _ := podOwner(k8sObjectMeta{OwnerReferences: []k8sOwnerReference{
		{Kind: "Deployment", Name: "web", Controller: false},
	}}, nil); kind != "" {
		t.Fatalf("非控制器引用不应算属主，实际 %q", kind)
	}
}

// 系统命名空间默认排除；开关打开后全量上报。这只影响清单，不影响指标。
func TestInventoryWantedExcludesSystemNamespaces(t *testing.T) {
	cfg := model.K8sInstanceConfig{}
	for _, ns := range []string{"kube-system", "kube-public", "kube-node-lease"} {
		if inventoryWanted(ns, cfg) {
			t.Fatalf("%s 应默认被排除", ns)
		}
	}
	if !inventoryWanted("default", cfg) {
		t.Fatal("业务命名空间应上报")
	}
	cfg.IncludeSystemNamespaces = true
	if !inventoryWanted("kube-system", cfg) {
		t.Fatal("开关打开后系统命名空间也应上报")
	}
}
