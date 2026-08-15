package alert

import (
	"testing"

	"github.com/nebula/monitor/internal/model"
)

func TestValidateSecurityEventRule(t *testing.T) {
	rule := model.AlertRule{
		Name:     "SSH 暴力破解",
		Type:     model.RuleTypeSecurityEvent,
		Category: model.SecurityCatSSHBruteforce,
		Severity: model.SeverityWarning,
		For:      "0s",
	}
	if err := ValidateRule(rule); err != nil {
		t.Fatalf("valid security event rule rejected: %v", err)
	}

	rule.Category = "unknown"
	if err := ValidateRule(rule); err == nil {
		t.Fatal("invalid security category accepted")
	}
}
