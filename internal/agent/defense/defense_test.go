package defense

import "testing"

func TestParseBannedIPs(t *testing.T) {
	got := parseBannedIPs(`Status for the jail: nebula-monitor-sshd
|- Currently banned: 2
|- Banned IP list: 192.0.2.1 2001:db8::1`)
	if len(got) != 2 || got[0] != "192.0.2.1" || got[1] != "2001:db8::1" {
		t.Fatalf("parseBannedIPs() = %#v", got)
	}
}

func TestParseBannedIPsEmpty(t *testing.T) {
	if got := parseBannedIPs("|- Currently banned: 0\n|- Banned IP list:"); len(got) != 0 {
		t.Fatalf("parseBannedIPs() = %#v, want empty", got)
	}
}

func TestJailTemplateUsesRealAndAuditActions(t *testing.T) {
	conf := jailConfTmpl
	if conf == "" {
		t.Fatal("jail template is empty")
	}
	if !containsAll(conf, "action = %s", "nebula-monitor-audit") {
		t.Fatalf("jail template does not contain composite actions: %s", conf)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		found := false
		for i := 0; i+len(p) <= len(s); i++ {
			if s[i:i+len(p)] == p {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
