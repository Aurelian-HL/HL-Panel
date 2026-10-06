package httpapi_test

import (
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/httpapi"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

func TestNezhaMonitoringRequiresAdministratorAndExistingGroup(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	hash, err := auth.HashPassword("nezha-http-password")
	if err != nil {
		t.Fatal(err)
	}
	store := memoryrepo.New(auth.Administrator{ID: "adm_nezha", Username: "admin", PasswordHash: hash, CreatedAt: now})
	handler := httpapi.New(
		auth.NewService(store, audit.NewService(store), clock, time.Hour),
		enrollment.NewService(store, clock, time.Hour),
		nodes.NewService(store, clock, 90*time.Second),
		groups.NewService(store, clock),
		endpoints.NewService(store, clock),
		generations.NewService(store, clock),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	path := "/api/v1/device-groups/missing/monitoring/nezha"
	requestJSON(t, handler, http.MethodGet, path, "", nil, http.StatusUnauthorized)
	requestJSON(t, handler, http.MethodGet, "/api/v1/monitoring/nezha/servers", "", nil, http.StatusUnauthorized)
	loginBody := requestJSON(t, handler, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "nezha-http-password"}, http.StatusOK)
	var login auth.LoginResult
	decodeResponse(t, loginBody, &login)
	requestJSON(t, handler, http.MethodGet, path, login.AccessToken, nil, http.StatusNotFound)
	group := createGroup(t, handler, login.AccessToken, "empty-probe-group", groups.KindEntry)
	body := requestJSON(t, handler, http.MethodGet, "/api/v1/device-groups/"+group.ID+"/monitoring/nezha", login.AccessToken, nil, http.StatusOK)
	var result struct {
		Items          []any  `json:"items"`
		UpstreamStatus string `json:"upstream_status"`
	}
	decodeResponse(t, body, &result)
	if result.UpstreamStatus != "disabled" || result.Items == nil || len(result.Items) != 0 {
		t.Fatalf("unexpected disabled monitoring response: %+v", result)
	}
	allBody := requestJSON(t, handler, http.MethodGet, "/api/v1/monitoring/nezha/servers", login.AccessToken, nil, http.StatusOK)
	decodeResponse(t, allBody, &result)
	if result.UpstreamStatus != "disabled" || result.Items == nil || len(result.Items) != 0 {
		t.Fatalf("unexpected disabled inventory response: %+v", result)
	}
}
