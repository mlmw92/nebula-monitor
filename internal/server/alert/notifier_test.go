package alert

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/config"
)

func TestWebhookNotifierReturnsHTTPFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	n := NewWebhookNotifier(config.WebhookConfig{Enabled: true, URLs: []string{srv.URL}})
	if err := n.Notify(model.AlertEvent{RuleName: "test", State: model.AlertStateFiring}); err == nil {
		t.Fatal("expected non-2xx webhook response to be reported as an error")
	}
}
