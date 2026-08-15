package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/server/audit"
)

func TestAuditRequestDetailSummarizesFieldsWithoutBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/rules", strings.NewReader(`{"name":"cpu","password":"secret"}`))
	req.Header.Set("Content-Type", "application/json")
	detail := auditRequestDetail(req)
	if !strings.Contains(detail, "request_fields=name,password") {
		t.Fatalf("unexpected detail: %s", detail)
	}
	if strings.Contains(detail, "secret") {
		t.Fatalf("detail leaked request value: %s", detail)
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`"password":"secret"`)) {
		t.Fatalf("request body was not restored: %s", body)
	}
}

func TestAuditMiddlewarePersistsChangeSummary(t *testing.T) {
	store := audit.New("")
	handler := AuditMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("handler could not read body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}), store)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/rules", strings.NewReader(`{"name":"cpu","password":"secret"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	events := store.List(1, "", "")
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if !strings.Contains(events[0].Detail, "request_fields=name,password") || strings.Contains(events[0].Detail, "secret") {
		t.Fatalf("unexpected audit detail: %s", events[0].Detail)
	}
}
