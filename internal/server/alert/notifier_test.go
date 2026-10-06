package alert

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/config"
)

func TestWebhookNotifierRedactsSecretsFromErrorsAndLogs(t *testing.T) {
	const secret = "sentinel-secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("upstream echoed token=" + secret))
	}))
	defer srv.Close()

	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(old)

	n := NewWebhookNotifier(config.WebhookConfig{Enabled: true, URLs: []string{srv.URL + "?token=" + secret}})
	err := n.Notify(model.AlertEvent{RuleName: "test", State: model.AlertStateFiring})
	if err == nil {
		t.Fatal("expected webhook failure")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(logs.String(), secret) {
		t.Fatalf("secret leaked; err=%q logs=%q", err, logs.String())
	}
}

func TestWebhookNotifierRedactsSecretFromConnectionError(t *testing.T) {
	const secret = "sentinel-secret"
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(old)

	n := NewWebhookNotifier(config.WebhookConfig{Enabled: true, URLs: []string{"http://127.0.0.1:1/hook?token=" + secret}})
	err := n.Notify(model.AlertEvent{RuleName: "test", State: model.AlertStateFiring})
	if err == nil {
		t.Fatal("expected connection failure")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(logs.String(), secret) {
		t.Fatalf("connection error leaked secret; err=%q logs=%q", err, logs.String())
	}
}

func TestWebhookGroupRedactsSecretsFromErrorsAndLogs(t *testing.T) {
	const secret = "sentinel-secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("token=" + secret))
	}))
	defer srv.Close()
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(old)
	n := NewWebhookNotifier(config.WebhookConfig{Enabled: true, URLs: []string{srv.URL + "/group?token=" + secret}})
	err := n.NotifyGroup([]model.AlertEvent{{RuleName: "test", State: model.AlertStateFiring}})
	if err == nil {
		t.Fatal("expected group webhook failure")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(logs.String(), secret) {
		t.Fatalf("group webhook leaked secret; err=%q logs=%q", err, logs.String())
	}
}

func TestRobotNotifierRedactsNon2xxBodyFromLogs(t *testing.T) {
	const secret = "sentinel-secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream token=" + secret))
	}))
	defer srv.Close()
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(old)
	err := postJSON(srv.URL+"?access_token="+secret, map[string]string{"msg": "test"})
	if err == nil {
		t.Fatal("expected non-2xx robot failure")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(logs.String(), secret) {
		t.Fatalf("robot non-2xx leaked secret; err=%q logs=%q", err, logs.String())
	}
}

func TestRobotNotifierRedactsSecretFromConnectionError(t *testing.T) {
	const secret = "sentinel-secret"
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(old)

	err := postJSON("http://127.0.0.1:1/hook?access_token="+secret, map[string]string{"msg": "test"})
	if err == nil {
		t.Fatal("expected connection failure")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(logs.String(), secret) {
		t.Fatalf("robot connection error leaked secret; err=%q logs=%q", err, logs.String())
	}
}

func TestRobotNotifierRedactsBusinessErrorMessage(t *testing.T) {
	const secret = "sentinel-secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errcode":400,"errmsg":"bad token ` + secret + `"}`))
	}))
	defer srv.Close()
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(old)

	err := postJSON(srv.URL+"?access_token="+secret, map[string]string{"msg": "test"})
	if err == nil {
		t.Fatal("expected business error")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(logs.String(), secret) {
		t.Fatalf("business error leaked secret: err=%q logs=%q", err, logs.String())
	}
}
