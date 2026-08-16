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

func TestParseSSHDEffectivePorts(t *testing.T) {
	cfg := "port 22\nport 3741 7465\n# port 9999\nPasswordAuthentication yes"
	ports := parseSSHDEffectivePorts(cfg)
	want := map[string]bool{"22": true, "3741": true, "7465": true}
	if len(ports) != 3 {
		t.Fatalf("parseSSHDEffectivePorts() = %#v, want 3 ports", ports)
	}
	for _, p := range ports {
		if !want[p] {
			t.Fatalf("unexpected port %q in %#v", p, ports)
		}
	}
}

func TestParseSSHDPortsFromConfig(t *testing.T) {
	cfg := "# sshd_config\nPort 3741\nPort 22\nPort 22\nMatch\nPort 3741"
	ports := parseSSHDPortsFromConfig(cfg)
	want := []string{"3741", "22"}
	if len(ports) != 2 || ports[0] != "3741" || ports[1] != "22" {
		t.Fatalf("parseSSHDPortsFromConfig() = %#v, want %#v", ports, want)
	}
}

func TestParseListenPortsFor(t *testing.T) {
	out := "LISTEN 0 128 0.0.0.0:3741 0.0.0.0:* users:((\"sshd\",pid=1,fd=3))\n" +
		"LISTEN 0 128 [::]:22 [::]:* users:((\"other\",pid=9,fd=3))\n" +
		"LISTEN 0 128 0.0.0.0:8080 0.0.0.0:* users:((\"nginx\",pid=2,fd=3))"
	ports := parseListenPortsFor(out, "sshd")
	if len(ports) != 1 || ports[0] != "3741" {
		t.Fatalf("parseListenPortsFor() = %#v, want [\"3741\"]", ports)
	}
}

func TestStrSliceContains(t *testing.T) {
	if !strSliceContains([]string{"a", "b"}, "b") {
		t.Fatal("strSliceContains should find b")
	}
	if strSliceContains([]string{"a", "b"}, "c") {
		t.Fatal("strSliceContains should not find c")
	}
}
