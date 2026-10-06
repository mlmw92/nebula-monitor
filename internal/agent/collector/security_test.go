package collector

import (
	"strings"
	"testing"
	"time"

	"github.com/nebula/monitor/internal/agent/config"
	"github.com/nebula/monitor/internal/model"
)

func newBruteforceTestCollector() *SecurityCollector {
	return &SecurityCollector{
		node:                 "test-node",
		cfg:                  config.SecurityConfig{BruteForceThreshold: 5, BruteForceWindowSec: 300},
		sshBruteforceAlertAt: map[string]int64{},
	}
}

func TestSSHBruteforceAggregatesSourcesIntoOneEvent(t *testing.T) {
	c := newBruteforceTestCollector()
	now := time.Now()
	counts := map[string]int{
		"203.0.113.9":   7,
		"198.51.100.10": 5,
		"192.0.2.77":    12,
		"10.0.0.5":      1, // 低于阈值，不计入
	}
	evs := c.bruteforceEvents(counts, now, 5*time.Minute)
	if len(evs) != 1 {
		t.Fatalf("多个来源应聚合为 1 条事件，got %d", len(evs))
	}
	ev := evs[0]
	if ev.Category != model.SecurityCatSSHBruteforce || ev.Severity != model.SeverityCritical {
		t.Fatalf("category/severity = %s/%s", ev.Category, ev.Severity)
	}
	for _, want := range []string{"3 个来源", "共失败 24 次", "203.0.113.9(7次)", "192.0.2.77(12次)"} {
		if !strings.Contains(ev.Message, want) {
			t.Fatalf("消息缺少 %q：%s", want, ev.Message)
		}
	}
	if strings.Contains(ev.Message, "10.0.0.5") {
		t.Fatalf("低于阈值的来源不应出现在消息中：%s", ev.Message)
	}
}

func TestSSHBruteforceAggregateIsThrottledPerWindow(t *testing.T) {
	c := newBruteforceTestCollector()
	now := time.Now()
	counts := map[string]int{"203.0.113.9": 7}
	if got := c.bruteforceEvents(counts, now, 5*time.Minute); len(got) != 1 {
		t.Fatalf("首次应发出 1 条事件")
	}
	// 窗口内换了新来源也不应再发（旧实现会按新 IP 各发一条，导致刷屏）
	newCounts := map[string]int{"198.51.100.10": 9}
	if got := c.bruteforceEvents(newCounts, now.Add(time.Minute), 5*time.Minute); len(got) != 0 {
		t.Fatalf("窗口内聚合事件应被节流，got %d 条", len(got))
	}
	if got := c.bruteforceEvents(counts, now.Add(6*time.Minute), 5*time.Minute); len(got) != 1 {
		t.Fatalf("窗口过后应再次发出")
	}
}

func TestSSHBruteforceNoOffendersEmitsNothing(t *testing.T) {
	c := newBruteforceTestCollector()
	if got := c.bruteforceEvents(map[string]int{"10.0.0.5": 2}, time.Now(), 5*time.Minute); len(got) != 0 {
		t.Fatalf("无达到阈值的来源时不应产生事件")
	}
}

func TestTrimSSHFailures(t *testing.T) {
	got := trimSSHFailures([]int64{100, 200, 300}, 200)
	if len(got) != 2 || got[0] != 200 || got[1] != 300 {
		t.Fatalf("trimSSHFailures() = %v, want [200 300]", got)
	}
}

func TestSSHBruteforceAlertIsRateLimitedByWindow(t *testing.T) {
	c := &SecurityCollector{sshBruteforceAlertAt: map[string]int64{}}
	window := 5 * time.Minute
	if !c.markSSHBruteforceAlert("198.51.100.10", 1_000, window) {
		t.Fatal("first aggregate alert should be emitted")
	}
	if c.markSSHBruteforceAlert("198.51.100.10", 2_000, window) {
		t.Fatal("duplicate aggregate alert within the window should be suppressed")
	}
	if !c.markSSHBruteforceAlert("198.51.100.10", 1_000+window.Milliseconds(), window) {
		t.Fatal("aggregate alert should be emitted again after the window")
	}
}

func TestSSHFailureCountsKeepsOnlyWindowEvents(t *testing.T) {
	c := &SecurityCollector{
		sshFailures:          map[string][]int64{"198.51.100.10": {100, 200, 300}},
		sshBruteforceAlertAt: map[string]int64{"198.51.100.10": 1},
	}
	counts := c.sshFailureCounts(200)
	if counts["198.51.100.10"] != 2 {
		t.Fatalf("failure count = %d, want 2", counts["198.51.100.10"])
	}
	c.sshFailureCounts(400)
	if _, ok := c.sshFailures["198.51.100.10"]; ok {
		t.Fatal("expired failures should be removed")
	}
	if _, ok := c.sshBruteforceAlertAt["198.51.100.10"]; ok {
		t.Fatal("expired alert timestamp should be removed")
	}
}
