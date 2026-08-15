package audit

import (
	"strings"
	"testing"
)

func TestSummarizeChangeReportsSemanticDiffWithoutSecrets(t *testing.T) {
	before := map[string]interface{}{
		"name":      "cpu-high",
		"threshold": 80,
		"password":  "old-secret",
	}
	after := map[string]interface{}{
		"name":      "cpu-high",
		"threshold": 90,
		"enabled":   true,
		"password":  "new-secret",
	}
	detail := SummarizeChange(before, after)
	if !containsAll(detail, "changed=threshold", "added=enabled", "removed=", "before_sha256=", "after_sha256=") {
		t.Fatalf("unexpected change summary: %s", detail)
	}
	if containsAll(detail, "old-secret", "new-secret") {
		t.Fatalf("change summary leaked secret: %s", detail)
	}
}

func containsAll(value string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}
