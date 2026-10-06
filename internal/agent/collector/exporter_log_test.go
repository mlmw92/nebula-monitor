package collector

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/model"
)

func TestExporterFailureLogRedactsURLCredentials(t *testing.T) {
	const secret = "sentinel-secret"
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(old)

	collector := NewMySQLCollector("node-a", []model.MySQLInstanceConfig{{
		Name: "mysql", Addr: "db:3306", ExporterURL: "http://user:" + secret + "@127.0.0.1:1/metrics?token=" + secret,
	}})
	collector.CollectCtx(context.Background())
	if strings.Contains(logs.String(), secret) {
		t.Fatalf("exporter log leaked URL credentials: %s", logs.String())
	}
}
