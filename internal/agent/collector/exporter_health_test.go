package collector

import (
	"strings"
	"testing"
)

func TestExporterHealthRequiresExpectedMetrics(t *testing.T) {
	for _, tc := range []struct {
		name        string
		text        string
		hasBusiness bool
		want        bool
	}{
		{name: "empty exposition"},
		{name: "comment only", text: "# HELP mysql_up whether the target is up\n"},
		{name: "business metrics present", hasBusiness: true, want: true},
		{name: "upstream up down", text: "mysql_up 0\n", hasBusiness: true},
		{name: "upstream up up", text: "mysql_up 1\n", want: true},
		{name: "platform up name down", text: "mysql_instance_up 0\n", hasBusiness: true},
		{name: "platform up name up", text: "mysql_instance_up 1\n", want: true},
		{name: "up only in comment", text: "# mysql_up 0\n", hasBusiness: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := exporterHealth(tc.text, tc.hasBusiness, "mysql_up", "mysql_instance_up")
			if got != tc.want {
				t.Fatalf("exporterHealth(%q, %v) = %v, want %v", tc.text, tc.hasBusiness, got, tc.want)
			}
		})
	}
}

func TestSafeExporterTargetRemovesCredentials(t *testing.T) {
	const secret = "sentinel-secret"
	for _, raw := range []string{
		"https://user:" + secret + "@example.com:9443/metrics?token=" + secret + "#frag",
		"https://example.com:9443/push/" + secret + "/metrics",
	} {
		got := safeExporterTarget(raw)
		if got != "https://example.com:9443" {
			t.Fatalf("target = %q", got)
		}
		if strings.Contains(got, secret) {
			t.Fatalf("secret leaked: %q", got)
		}
	}
}
