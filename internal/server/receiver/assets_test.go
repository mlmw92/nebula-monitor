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
