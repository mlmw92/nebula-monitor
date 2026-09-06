package collector

import (
	"testing"
	"time"
)

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
