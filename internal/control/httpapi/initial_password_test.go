package httpapi_test

import (
	"bytes"
	"encoding/json"
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
	"github.com/hongle/hl-panel/internal/control/panelupdate"
	"github.com/hongle/hl-panel/internal/control/releases"
	controlusage "github.com/hongle/hl-panel/internal/control/usage"
	usagememory "github.com/hongle/hl-panel/internal/control/usage/memory"
)

func TestInitialPasswordRequiresDurableChangeBeforeBusinessAccess(t *testing.T) {
	now := time.Now().UTC()
	hash, err := auth.HashPassword(auth.DefaultPassword)
	if err != nil {
		t.Fatal(err)
	}
	if !auth.IsDefaultPasswordHash(hash) {
		t.Fatal("default hash not recognized")
	}
	store := memoryrepo.New(auth.Administrator{ID: "adm_initial", Username: "customadmin", PasswordHash: hash, CreatedAt: now, MustChangePassword: true})
	makeHandler := func(s *memoryrepo.Store) http.Handler {
		authService := auth.NewService(s, audit.NewService(s), time.Now, time.Hour)
		versions := releases.New("development")
		return httpapi.New(auth.NewService(s, audit.NewService(s), time.Now, time.Hour),
			enrollment.NewService(s, time.Now, time.Hour), nodes.NewService(s, time.Now, time.Minute),
			groups.NewService(s, time.Now), endpoints.NewService(s, time.Now), generations.NewService(s, time.Now),
			slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.WithReleases(versions), httpapi.WithPanelUpdate(panelupdate.New(authService, audit.NewService(s), versions)), httpapi.WithUsage(controlusage.NewService(usagememory.New(), loopbackPolicy{limit: 100}, time.Now)))
	}
	handler := makeHandler(store)
	var login auth.LoginResult
	decodeResponse(t, requestJSON(t, handler, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"username": "customadmin", "password": auth.DefaultPassword}, http.StatusOK), &login)
	if !login.User.MustChangePassword {
		t.Fatal("login missing password change requirement")
	}
	// Persist both the requirement and live session, as PostgreSQL does.
	raw, err := store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	store, err = memoryrepo.DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	handler = makeHandler(store)
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/overview"}, {http.MethodGet, "/api/v1/nodes"},
		{http.MethodPost, "/api/v1/device-groups"},
		{http.MethodGet, "/api/v1/usage"},
		{http.MethodPost, "/api/v1/usage/enforcement/test/revoke"},
		{http.MethodGet, "/api/v1/panel/update"}, {http.MethodPost, "/api/v1/panel/update"},
	} {
		body := requestJSON(t, handler, route.method, route.path, login.AccessToken, nil, http.StatusForbidden)
		if !bytes.Contains(body, []byte("password_change_required")) {
			t.Fatal("incorrect restriction")
		}
	}
	requestJSON(t, handler, http.MethodGet, "/api/v1/auth/me", login.AccessToken, nil, http.StatusOK)
	requestJSON(t, handler, http.MethodGet, "/api/v1/system/version", "", nil, http.StatusUnauthorized)
	requestJSON(t, handler, http.MethodGet, "/api/v1/panel/update", "", nil, http.StatusUnauthorized)
	requestJSON(t, handler, http.MethodPost, "/api/v1/panel/update", "", nil, http.StatusUnauthorized)
	requestJSON(t, handler, http.MethodGet, "/api/v1/system/version", login.AccessToken, nil, http.StatusOK)
	for _, input := range []map[string]string{
		{"current_password": "wrong", "new_password": "replacement-secret"},
		{"current_password": auth.DefaultPassword, "new_password": auth.DefaultPassword},
	} {
		requestJSON(t, handler, http.MethodPut, "/api/v1/auth/password", login.AccessToken, input, http.StatusBadRequest)
	}
	requestJSON(t, handler, http.MethodPut, "/api/v1/auth/password", login.AccessToken, map[string]string{"current_password": auth.DefaultPassword, "new_password": "replacement-secret"}, http.StatusNoContent)
	requestJSON(t, handler, http.MethodGet, "/api/v1/auth/me", login.AccessToken, nil, http.StatusUnauthorized)
	raw, err = store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	store, err = memoryrepo.DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	handler = makeHandler(store)
	requestJSON(t, handler, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"username": "customadmin", "password": auth.DefaultPassword}, http.StatusUnauthorized)
	decodeResponse(t, requestJSON(t, handler, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"username": "customadmin", "password": "replacement-secret"}, http.StatusOK), &login)
	if login.User.MustChangePassword {
		t.Fatal("password change did not unlock account")
	}
	requestJSON(t, handler, http.MethodGet, "/api/v1/overview", login.AccessToken, nil, http.StatusOK)
	events, _ := json.Marshal(store.AuditEvents())
	if !bytes.Contains(events, []byte("auth.password.change")) || bytes.Contains(events, []byte("replacement-secret")) {
		t.Fatal("invalid password audit")
	}
}
