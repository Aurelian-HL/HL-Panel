package httpapi_test

import (
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/diagnostics"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/httpapi"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

func TestLocalDiagnosticsRequireAdministratorAndFixedTarget(t *testing.T) {
	now := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	hash, err := auth.HashPassword("local-test-password")
	if err != nil {
		t.Fatal(err)
	}
	store := memoryrepo.New(auth.Administrator{ID: "admin-local", Username: "admin", PasswordHash: hash, CreatedAt: now})
	clock := func() time.Time { return now }
	targets, err := diagnostics.ParseTargets("Public resolver|1.1.1.1")
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.New(auth.NewService(store, audit.NewService(store), clock, time.Hour), enrollment.NewService(store, clock, time.Hour), nodes.NewService(store, clock, time.Minute), groups.NewService(store, clock), endpoints.NewService(store, clock), generations.NewService(store, clock), slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.WithDiagnostics(diagnostics.NewService(targets, audit.NewService(store), nil, nil, clock)))
	requestJSON(t, handler, http.MethodGet, "/api/v1/diagnostics/targets", "", nil, http.StatusUnauthorized)
	requestJSON(t, handler, http.MethodPost, "/api/v1/diagnostics/run", "", map[string]string{"target_id": "target-1", "action": "ping"}, http.StatusUnauthorized)
	var login auth.LoginResult
	decodeResponse(t, requestJSON(t, handler, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"username": "admin", "password": "local-test-password"}, http.StatusOK), &login)
	var listed struct {
		Scope string               `json:"scope"`
		Items []diagnostics.Target `json:"items"`
	}
	decodeResponse(t, requestJSON(t, handler, http.MethodGet, "/api/v1/diagnostics/targets", login.AccessToken, nil, http.StatusOK), &listed)
	if listed.Scope != "control-plane-local" || len(listed.Items) != 1 || listed.Items[0].Label != "Public resolver" {
		t.Fatalf("unexpected targets: %+v", listed)
	}
	requestJSON(t, handler, http.MethodPost, "/api/v1/diagnostics/run", login.AccessToken, map[string]string{"target_id": "arbitrary", "action": "ping"}, http.StatusBadRequest)
	requestJSON(t, handler, http.MethodPost, "/api/v1/diagnostics/run", login.AccessToken, map[string]string{"target_id": "target-1", "action": "shell"}, http.StatusBadRequest)
	requestJSON(t, handler, http.MethodPost, "/api/v1/diagnostics/run", login.AccessToken, map[string]string{"target_id": "target-1", "action": "dns"}, http.StatusBadRequest)
	requestJSON(t, handler, http.MethodPost, "/api/v1/diagnostics/run", login.AccessToken, map[string]string{"unexpected": "field"}, http.StatusBadRequest)
	denied := 0
	for _, event := range store.AuditEvents() {
		if event.Action == "diagnostics.run" && event.Outcome == "denied" {
			denied++
		}
	}
	if denied != 4 {
		t.Fatalf("expected four denied diagnostic audit events, got %d", denied)
	}
}
