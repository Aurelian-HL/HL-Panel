package httpapi_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/announcements"
	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/httpapi"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/control/siteconfig"
)

func TestSiteSettingsAndAnnouncementsPublicProjection(t *testing.T) {
	now := time.Date(2026, time.October, 3, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	hash, err := auth.HashPassword("isolated-site-administrator")
	if err != nil {
		t.Fatal(err)
	}
	store := memoryrepo.New(auth.Administrator{ID: "adm-site", Username: "admin", PasswordHash: hash, CreatedAt: now})
	handler := httpapi.New(
		auth.NewService(store, audit.NewService(store), clock, time.Hour), enrollment.NewService(store, clock, time.Hour),
		nodes.NewService(store, clock, time.Minute), groups.NewService(store, clock), endpoints.NewService(store, clock),
		generations.NewService(store, clock), slog.New(slog.NewTextHandler(io.Discard, nil)),
		httpapi.WithSite(siteconfig.NewService(store, clock), announcements.NewService(store, clock), httpapi.PlatformInfo{Version: "test-version", BuildTime: "2026-10-03T00:00:00Z"}),
	)
	var defaults struct {
		Settings siteconfig.Settings `json:"settings"`
		Version  string              `json:"platform_version"`
	}
	decodeResponse(t, requestJSON(t, handler, http.MethodGet, "/api/v1/public/site-info", "", nil, http.StatusOK), &defaults)
	if defaults.Settings.SiteName != "HL-panel" || defaults.Settings.PanelTitle != "HL-panel" || defaults.Version != "test-version" {
		t.Fatalf("unexpected defaults: %+v", defaults)
	}
	requestJSON(t, handler, http.MethodGet, "/api/v1/site-settings", "", nil, http.StatusUnauthorized)
	var login auth.LoginResult
	decodeResponse(t, requestJSON(t, handler, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "isolated-site-administrator"}, http.StatusOK), &login)
	settings := siteconfig.Request{SiteName: "鸿乐线路", PanelTitle: "线路管理面板", Theme: siteconfig.ThemeClassic, Revision: 0}
	mutatingJSON(t, handler, http.MethodPut, "/api/v1/site-settings", login.AccessToken, "settings-1", settings, http.StatusOK)
	announcement := announcements.Request{Title: "维护提醒", Content: "今晚维护", Level: announcements.LevelMaintenance, Enabled: true, Revision: 0}
	mutatingJSON(t, handler, http.MethodPost, "/api/v1/announcements", login.AccessToken, "announcement-1", announcement, http.StatusCreated)
	var public struct {
		Settings      siteconfig.Settings          `json:"settings"`
		Announcements []announcements.Announcement `json:"announcements"`
	}
	decodeResponse(t, requestJSON(t, handler, http.MethodGet, "/api/v1/public/site-info", "", nil, http.StatusOK), &public)
	if public.Settings.SiteName != "鸿乐线路" || len(public.Announcements) != 1 || public.Announcements[0].Title != "维护提醒" {
		t.Fatalf("public projection did not reflect durable settings: %+v", public)
	}
}

func mutatingJSON(t *testing.T, handler http.Handler, method, path, token, key string, body any, status int) []byte {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Idempotency-Key", key)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != status {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, path, recorder.Code, status, recorder.Body.String())
	}
	return recorder.Body.Bytes()
}
