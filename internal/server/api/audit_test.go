package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/server/audit"
	"github.com/nebula/monitor/internal/server/config"
)

func TestHandleAuditEventsRequiresAuthentication(t *testing.T) {
	a := &API{auth: config.AuthConfig{Enabled: true}, audit: audit.New("")}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/events", nil)
	rec := httptest.NewRecorder()
	a.handleAuditEvents(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestHandleAuditEventsReturnsFilteredEvents(t *testing.T) {
	store := audit.New("")
	if err := store.Record(audit.Event{User: "admin", Method: http.MethodPost, Path: "/api/v1/rules", Status: http.StatusOK, Succeeded: true, Category: "management", Action: "create_or_apply /api/v1/rules"}); err != nil {
		t.Fatal(err)
	}
	a := &API{auth: config.AuthConfig{Enabled: false}, audit: store}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/events?limit=10&user=admin&path=rules&category=management", nil)
	rec := httptest.NewRecorder()
	a.handleAuditEvents(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body struct {
		Events []audit.Event `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Events) != 1 || body.Events[0].Action == "" {
		t.Fatalf("unexpected events: %#v", body.Events)
	}
}

func TestHandleAuditEventsExportsCSV(t *testing.T) {
	store := audit.New("")
	if err := store.Record(audit.Event{User: "admin", Method: http.MethodPost, Path: "/api/v1/rules", Status: http.StatusOK, Succeeded: true, Category: "management", Action: "create_or_apply /api/v1/rules"}); err != nil {
		t.Fatal(err)
	}
	a := &API{auth: config.AuthConfig{Enabled: false}, audit: store}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/events?format=csv&category=management", nil)
	rec := httptest.NewRecorder()
	a.handleAuditEvents(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Disposition"); got == "" {
		t.Fatal("expected CSV content disposition")
	}
	if !strings.Contains(rec.Body.String(), "category") || !strings.Contains(rec.Body.String(), "create_or_apply") {
		t.Fatalf("unexpected CSV body: %s", rec.Body.String())
	}
}
