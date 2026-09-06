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

func TestShouldAlertSecurityEvent(t *testing.T) {
	cases := []struct {
		name  string
		event model.SecurityEvent
		want  bool
	}{
		{name: "SSH 登录失败", event: model.SecurityEvent{Category: model.SecurityCatSSHAudit, Detail: map[string]string{"result": "failed"}}, want: false},
		{name: "SSH 无效用户", event: model.SecurityEvent{Category: model.SecurityCatSSHAudit, Detail: map[string]string{"result": "invalid"}}, want: false},
		{name: "SSH 登录成功", event: model.SecurityEvent{Category: model.SecurityCatSSHAudit, Detail: map[string]string{"result": "success"}}, want: true},
		{name: "SSH 暴力破解", event: model.SecurityEvent{Category: model.SecurityCatSSHBruteforce}, want: true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldAlertSecurityEvent(tt.event); got != tt.want {
				t.Fatalf("shouldAlertSecurityEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCleanupInactiveSecurityRulesPreservesEventsForEnabledRules(t *testing.T) {
	disabledAll := model.AlertRule{ID: "disabled-all", Type: model.RuleTypeSecurityEvent, Enabled: false}
	enabledSSH := model.AlertRule{
		ID:       "enabled-ssh",
		Type:     model.RuleTypeSecurityEvent,
		Category: model.SecurityCatSSHAudit,
		Enabled:  true,
	}
	engine := &Engine{
		securityEvents: map[string][]model.SecurityEvent{
			"node-1": {
				{ID: "ssh-event", Node: "node-1", Category: model.SecurityCatSSHAudit},
				{ID: "fim-event", Node: "node-1", Category: model.SecurityCatFIM},
			},
		},
		securityRuleEvents: map[string]int64{
			"disabled-all|node-1|old-event": 1,
		},
		firing: map[string]*firingEntry{},
		states: map[string]*ruleState{},
	}

	engine.cleanupInactiveRulesLocked([]model.AlertRule{disabledAll, enabledSSH}, model.NowMillis())

	events := engine.securityEvents["node-1"]
	if len(events) != 1 || events[0].ID != "ssh-event" {
		t.Fatalf("remaining events = %+v, want only SSH event", events)
	}
	if len(engine.securityRuleEvents) != 0 {
		t.Fatalf("disabled rule dedup entries were not cleared: %+v", engine.securityRuleEvents)
	}
}
