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

// Service 清单采集：把 apiserver 的 Service + EndpointSlice 变成台账清单与后端 Pod 名。
//
// 覆盖四个必须做对、且都容易写错的点：
//  1. **后端来自 EndpointSlice 的 targetRef**，不是 selector 命中（未就绪的 Pod 不在 Endpoints 里）；
//  2. **只收 kind=Pod 的 targetRef**：手写的 Endpoints（没有 targetRef）与指向别的对象一律跳过——
//     宁可少一条边，也不要一条指错的边；
//  3. **同一后端出现在多个切片里要去重**（滚动更新期间很常见）；
//  4. **Headless 的 clusterIP 是字面量 "None"**，不是地址；ExternalName 压根没有这个字段。
func TestK8sCollectorBuildsServiceInventoryWithBackends(t *testing.T) {
	srv := fakeK8sAPIServerWith(t, []any{
		serviceJSON("default", "web", "ClusterIP", "10.43.0.10"),
		serviceJSON("default", "headless", "ClusterIP", "None"),
		serviceJSON("default", "external", "ExternalName", ""),
		serviceJSON("kube-system", "kube-dns", "ClusterIP", "10.43.0.11"),
	}, []any{
		// web 的后端落在两个切片里（分片），其中一个 Pod 重复出现
		endpointSliceJSON("default", "web", []any{
			targetRef("Pod", "default", "web-7d9f-abc"),
			targetRef("Pod", "default", "web-7d9f-def"),
		}),
		endpointSliceJSON("default", "web", []any{
			targetRef("Pod", "default", "web-7d9f-abc"), // 重复：去重后不应出现两次
			targetRef("Node", "", "worker-01"),          // 非 Pod：跳过
			map[string]any{},                            // 没有 targetRef（手写 Endpoints）：跳过
			targetRef("Pod", "other-ns", "someone"),     // 跨命名空间：跳过
		}),
		// 系统命名空间的切片同样不进清单
		endpointSliceJSON("kube-system", "kube-dns", []any{targetRef("Pod", "kube-system", "coredns-1")}),
	})

	c := NewK8sCollector("worker-01", []model.K8sInstanceConfig{{
		Name: "dev-k8s", APIServer: srv.URL, Token: "test-token",
	}})
	res := c.CollectCtx(context.Background())

	if res.ServicesTruncated {
		t.Fatalf("小集群不应触发截断：%+v", res)
	}
	if len(res.Services) != 3 {
		t.Fatalf("系统命名空间的 Service 应被排除，期望 3 个，实际 %d：%+v", len(res.Services), res.Services)
	}
	web := findService(t, res.Services, "web")
	if web.Type != "ClusterIP" || web.ClusterIP != "10.43.0.10" {
		t.Fatalf("web 的类型与地址不符：%+v", web)
	}
	if len(web.BackendPods) != 2 || web.BackendPods[0] != "web-7d9f-abc" || web.BackendPods[1] != "web-7d9f-def" {
		t.Fatalf("web 的后端应去重且有序：%+v", web.BackendPods)
	}
	if web.BackendsTruncated {
		t.Fatalf("两个后端不该触发截断：%+v", web)
	}

	// Headless：clusterIP 是字面量 "None"，不能当成地址带出去
	if h := findService(t, res.Services, "headless"); h.ClusterIP != "" {
		t.Fatalf("Headless 的 clusterIP 应为空，实际 %q", h.ClusterIP)
	}
	// ExternalName：没有地址，也没有后端
	ext := findService(t, res.Services, "external")
	if ext.ClusterIP != "" || len(ext.BackendPods) != 0 {
		t.Fatalf("ExternalName 不应有地址与后端：%+v", ext)
	}
}

// 单服务后端上限：超限**显式置位**，不静默截断。
func TestK8sCollectorTruncatesServiceBackends(t *testing.T) {
	eps := make([]any, 0, model.K8sServiceMaxBackends+1)
	for i := 0; i <= model.K8sServiceMaxBackends; i++ {
		eps = append(eps, targetRef("Pod", "default", "pod-"+strconv.Itoa(i)))
	}
	srv := fakeK8sAPIServerWith(t,
		[]any{serviceJSON("default", "big", "ClusterIP", "10.43.0.20")},
		[]any{endpointSliceJSON("default", "big", eps)},
	)

	c := NewK8sCollector("worker-01", []model.K8sInstanceConfig{{
		Name: "dev-k8s", APIServer: srv.URL, Token: "test-token",
	}})
	res := c.CollectCtx(context.Background())

	if len(res.Services) != 1 {
		t.Fatalf("期望 1 个 Service，实际 %d", len(res.Services))
	}
	svc := res.Services[0]
	if len(svc.BackendPods) != model.K8sServiceMaxBackends {
		t.Fatalf("后端应被截到 %d 个，实际 %d", model.K8sServiceMaxBackends, len(svc.BackendPods))
	}
	if !svc.BackendsTruncated {
		t.Fatalf("超限必须显式置位 BackendsTruncated，否则中心会以为这个服务只有 %d 个后端",
			model.K8sServiceMaxBackends)
	}
}

// 整份上报级上限：Service 数超限置位 ServicesTruncated（与 Pod/Workload 同口径）。
func TestK8sCollectorTruncatesServiceListAtReportLimit(t *testing.T) {
	services := make([]any, 0, model.K8sServicesMaxPerReport+1)
	for i := 0; i <= model.K8sServicesMaxPerReport; i++ {
		services = append(services, serviceJSON("default", "svc-"+strconv.Itoa(i), "ClusterIP", "10.43.0.30"))
	}
	srv := fakeK8sAPIServerWith(t, services, nil)

	c := NewK8sCollector("worker-01", []model.K8sInstanceConfig{{
		Name: "dev-k8s", APIServer: srv.URL, Token: "test-token",
	}})
	res := c.CollectCtx(context.Background())

	if len(res.Services) != model.K8sServicesMaxPerReport {
		t.Fatalf("应截到 %d 个，实际 %d", model.K8sServicesMaxPerReport, len(res.Services))
	}
	if !res.ServicesTruncated {
		t.Fatalf("超限必须显式置位 ServicesTruncated")
	}
}

// EndpointSlice 拿不到时**不是致命错误**：Service 资产照落，只是这次不建关系。
// 这条很要紧——它决定了"没有权限读 discovery API"的集群不会连 Service 清单都拿不到。
func TestK8sCollectorKeepsServicesWhenEndpointSlicesUnavailable(t *testing.T) {
	srv := fakeK8sAPIServerWith(t,
		[]any{serviceJSON("default", "web", "ClusterIP", "10.43.0.10")},
		nil, // 没有切片：假 apiserver 对 /apis/discovery.k8s.io/v1/endpointslices 回 404
	)

	c := NewK8sCollector("worker-01", []model.K8sInstanceConfig{{
		Name: "dev-k8s", APIServer: srv.URL, Token: "test-token",
	}})
	res := c.CollectCtx(context.Background())

	if len(res.Services) != 1 || res.Services[0].Name != "web" {
		t.Fatalf("拿不到 EndpointSlice 时仍应落 Service 清单：%+v", res.Services)
	}
	if len(res.Services[0].BackendPods) != 0 {
		t.Fatalf("没有切片就不该有后端（不猜）：%+v", res.Services[0].BackendPods)
	}
}

// ---- 夹具 ----

// fakeK8sAPIServerWith 只回清单相关路径的假 apiserver；slices 为 nil 时不注册切片路径（模拟 404）。
func fakeK8sAPIServerWith(t *testing.T, services, slices []any) *httptest.Server {
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
	mux.HandleFunc("/api/v1/nodes", empty)
	mux.HandleFunc("/api/v1/pods", empty)
	mux.HandleFunc("/api/v1/services", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"items": services})
	})
	if slices != nil {
		mux.HandleFunc("/apis/discovery.k8s.io/v1/endpointslices", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, map[string]any{"items": slices})
		})
	}
	for _, p := range []string{"/apis/apps/v1/deployments", "/apis/apps/v1/statefulsets", "/apis/apps/v1/daemonsets", "/apis/apps/v1/replicasets"} {
		mux.HandleFunc(p, empty)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func serviceJSON(namespace, name, typ, clusterIP string) map[string]any {
	spec := map[string]any{"type": typ}
	if clusterIP != "" {
		spec["clusterIP"] = clusterIP
	}
	return map[string]any{
		"metadata": map[string]any{"name": name, "namespace": namespace},
		"spec":     spec,
	}
}

// endpointSliceJSON 造一个切片；服务名走真实存在的标签 `kubernetes.io/service-name`。
func endpointSliceJSON(namespace, service string, endpoints []any) map[string]any {
	return map[string]any{
		"metadata": map[string]any{
			"name": service + "-abc", "namespace": namespace,
			"labels": map[string]string{"kubernetes.io/service-name": service},
		},
		"endpoints": endpoints,
	}
}

func targetRef(kind, namespace, name string) map[string]any {
	ref := map[string]any{"kind": kind, "name": name}
	if namespace != "" {
		ref["namespace"] = namespace
	}
	return map[string]any{"targetRef": ref}
}

func findService(t *testing.T, services []model.K8sService, name string) model.K8sService {
	t.Helper()
	for _, s := range services {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("清单里没有 Service %s：%+v", name, services)
	return model.K8sService{}
}
