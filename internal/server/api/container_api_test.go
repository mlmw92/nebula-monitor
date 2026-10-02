package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/instancereg"
)

// containerStore 复用 clusterStore 的全部替身方法，只补 QueryAllLatest——
// 集群清单的在线状态取自它（clusterStore 的那一版恒返回 nil，只够中间件接口用）。
type containerStore struct {
	clusterStore
	all map[string][]model.Series
}

func (s *containerStore) QueryAllLatest(name string, _ map[string]string) ([]model.Series, error) {
	return s.all[name], nil
}

// GET /api/v1/container/k8s/clusters 的三条底线：
//  1. 权限点是 container:read，**不能**被 middleware:read 顶替（两个信息面刻意分开）；
//  2. 按资源范围过滤——范围外的集群连名字都不返回（只给名字也等于暴露了资产存在）；
//  3. 返回体里永远不含凭据字段（kubeconfig/token 只在 Agent 本地）。
func TestRoutes_ContainerClusters(t *testing.T) {
	const (
		nodeA = "web-01"
		nodeB = "db-01"
		instA = "https://10.0.0.1:6443"
	)
	instancereg.Default.SetK8s(nodeA, []model.K8sInstance{
		{Node: nodeA, Instance: instA, Name: "prod-k8s", Version: "v1.29.0", Group: "g1"},
	})
	instancereg.Default.SetK8s(nodeB, []model.K8sInstance{
		{Node: nodeB, Instance: "https://10.0.0.2:6443", Name: "db-k8s", Version: "v1.29.0", Group: "g2"},
	})
	t.Cleanup(func() {
		// instancereg.Default 是包级单例：不清理会污染同包其它用例。
		instancereg.Default.SetK8s(nodeA, nil)
		instancereg.Default.SetK8s(nodeB, nil)
	})

	a := scopeTestAPI(t)
	a.store = &containerStore{all: map[string][]model.Series{
		"k8s_cluster_up": {grSeries(map[string]string{"node": nodeA, "instance": instA}, 1)},
	}}
	mux := newRoutesMux(a)

	// 1) 只有 middleware:read → 拒绝
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(globalPrincipal("middleware:read"), http.MethodGet, "/api/v1/container/k8s/clusters", ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("middleware:read 不得顶替 container:read，code = %d，body = %s", rec.Code, rec.Body.String())
	}

	// 2) 有 container:read 但范围只有 g1 → 只返回 g1 上的集群
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"container:read"}, "g1"), http.MethodGet, "/api/v1/container/k8s/clusters", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("范围内应放行，code = %d，body = %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	list, _ := body["clusters"].([]any)
	if len(list) != 1 {
		t.Fatalf("范围外集群不得返回，实际 %v", body["clusters"])
	}
	first, _ := list[0].(map[string]any)
	if first["name"] != "prod-k8s" {
		t.Fatalf("应返回 prod-k8s，实际 %v", first)
	}
	// 在线状态来自 k8s_cluster_up 的最新样本；这条是"有没有数据"而不是"配置里有没有"
	if first["up"] != true {
		t.Fatalf("web-01 的集群应判在线，实际 %v", first["up"])
	}
	if first["node"] != nodeA || first["instance"] != instA {
		t.Fatalf("节点/实例应原样回传，实际 %v", first)
	}

	// 3) 凭据边界：返回体里不能出现任何凭据字段名
	raw := rec.Body.String()
	for _, banned := range []string{"token", "kubeconfig", "secret", "password"} {
		if strings.Contains(strings.ToLower(raw), banned) {
			t.Fatalf("集群清单不得含凭据字段 %q：%s", banned, raw)
		}
	}
}

// 空结果必须是 `[]` 而不是 `null`：否则前端每个用到它的地方都要多写一层判空。
func TestRoutes_ContainerClusters_EmptyIsArray(t *testing.T) {
	a := scopeTestAPI(t)
	a.store = &clusterStore{}
	mux := newRoutesMux(a)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(globalPrincipal("container:read"), http.MethodGet, "/api/v1/container/k8s/clusters", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("无集群配置也应 200，code = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"clusters":[]`) {
		t.Fatalf("空结果应是 [] 而非 null，实际 %s", rec.Body.String())
	}
}
