package collector

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/nebula/monitor/internal/agent/config"
)

// goroutineCount 返回当前 goroutine 数。
func goroutineCount() int { return runtime.NumGoroutine() }

// waitGoroutinesBelow 在 1s 内等待 goroutine 数降到 limit 以下，返回最终数量。
func waitGoroutinesBelow(limit int) int {
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		if runtime.NumGoroutine() <= limit {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return runtime.NumGoroutine()
}

// newTestCollector 构造一个「只开启主机采集」的 Collector，避免测试依赖任何中间件。
func newTestCollector(t *testing.T, timeout time.Duration) *Collector {
	t.Helper()
	cfg := config.CollectorToggle{CPU: true, Memory: true, Disk: true}
	return New("test-node", "default", nil, cfg,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		config.SecurityConfig{}, timeout, nil, config.TemplateGuardsConfig{})
}

// TestCollectAll_TaskListIsComplete 断言任务清单完整，且防火墙规则与状态被合并为单个任务
// （两者各自会 exec 探测防火墙后端，合并后同一轮不重复探测）。
func TestCollectAll_TaskListIsComplete(t *testing.T) {
	c := newTestCollector(t, time.Second)
	var res Result
	var mu sync.Mutex
	tasks := c.tasks(&res, &mu)

	names := map[string]int{}
	for _, task := range tasks {
		names[task.name]++
	}

	want := []string{
		"host", "redis", "mysql", "postgres", "nginx", "nginx-access",
		"kafka", "docker", "rocketmq", "k8s", "mongodb", "fastdfs",
		"security", "listeners", "firewall", "host-info",
	}
	for _, n := range want {
		if names[n] != 1 {
			t.Errorf("任务 %q 期望出现 1 次，实际 %d 次", n, names[n])
		}
	}
	if len(tasks) != len(want) {
		t.Errorf("任务总数期望 %d，实际 %d（可能存在多余任务）", len(want), len(tasks))
	}
	if names["firewall"] != 1 {
		t.Fatalf("防火墙规则与状态未被合并为单任务，firewall 任务数 = %d", names["firewall"])
	}
}

// TestCollectAll_AllTasksFail_StillReturnsHostMetrics 宿主指标不应因中间件全部失败而丢失。
func TestCollectAll_AllTasksFail_StillReturnsHostMetrics(t *testing.T) {
	c := newTestCollector(t, 3*time.Second)

	res := c.CollectAll(context.Background())

	if len(res.Metrics) == 0 {
		t.Fatal("期望仍返回主机指标，实际为空")
	}
	found := false
	for _, m := range res.Metrics {
		if m.Name == "cpu_cores" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("主机指标中缺少 cpu_cores")
	}
	// 未启用任何中间件时，实例列表应为空而不是报错
	if len(res.Redis) != 0 || len(res.MySQL) != 0 || len(res.Docker) != 0 {
		t.Fatalf("未启用中间件采集时不应产生实例：redis=%d mysql=%d docker=%d",
			len(res.Redis), len(res.MySQL), len(res.Docker))
	}
}

// TestCollectAll_TimeoutZeroMeansNoTaskTimeout 验证 timeout=0 时不施加任务级 deadline，
// 采集仍能正常完成（与旧版串行行为等价）。
func TestCollectAll_TimeoutZeroMeansNoTaskTimeout(t *testing.T) {
	c := newTestCollector(t, 0)

	res := c.CollectAll(context.Background())

	if len(res.Metrics) == 0 {
		t.Fatal("timeout=0 时主机采集应正常完成，实际无指标")
	}
}

// TestCollectAll_CtxAlreadyCanceledReturnsPromptly 已取消的父 ctx 应让本轮采集快速返回，
// 且不产生主机指标（入口门控生效）。
func TestCollectAll_CtxAlreadyCanceledReturnsPromptly(t *testing.T) {
	c := newTestCollector(t, 3*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	res := c.CollectAll(ctx)
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Fatalf("已取消的 ctx 未让采集快速返回，耗时 %v", elapsed)
	}
	if len(res.Metrics) != 0 {
		t.Fatalf("ctx 已取消时不应产生指标，实际 %d 条", len(res.Metrics))
	}
}

// TestCollectAll_NoGoroutineLeak 一轮完整采集结束后不应遗留采集 goroutine。
func TestCollectAll_NoGoroutineLeak(t *testing.T) {
	c := newTestCollector(t, 100*time.Millisecond)
	baseline := goroutineCount()

	for i := 0; i < 3; i++ {
		_ = c.CollectAll(context.Background())
	}

	if after := waitGoroutinesBelow(baseline + 3); after > baseline+3 {
		t.Fatalf("疑似 goroutine 泄漏：基线 %d，结束后 %d", baseline, after)
	}
}
