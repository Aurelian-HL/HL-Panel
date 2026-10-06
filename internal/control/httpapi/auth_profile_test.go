package httpapi_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
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

func TestAdministratorProfileAndPasswordChange(t *testing.T) {
	now := time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC)
	oldPassword := "previous-secret-123"
	newPassword := "replacement-secret-456"
	hash, err := auth.HashPassword(oldPassword)
	if err != nil {
		t.Fatal(err)
	}
	store := memoryrepo.New(auth.Administrator{ID: "adm_profile", Username: "admin", PasswordHash: hash, CreatedAt: now})
	clock := func() time.Time { return now }
	handler := httpapi.New(
		auth.NewService(store, audit.NewService(store), clock, time.Hour),
		enrollment.NewService(store, clock, time.Hour),
		nodes.NewService(store, clock, 90*time.Second),
		groups.NewService(store, clock),
		endpoints.NewService(store, clock),
		generations.NewService(store, clock),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	requestJSON(t, handler, http.MethodGet, "/api/v1/auth/me", "", nil, http.StatusUnauthorized)
	var login auth.LoginResult
	decodeResponse(t, requestJSON(t, handler, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"username": "admin", "password": oldPassword}, http.StatusOK), &login)
	var profile struct {
		User auth.AdministratorView `json:"user"`
	}
	body := requestJSON(t, handler, http.MethodGet, "/api/v1/auth/me", login.AccessToken, nil, http.StatusOK)
	decodeResponse(t, body, &profile)
	if profile.User.ID != "adm_profile" || profile.User.Username != "admin" || !profile.User.CreatedAt.Equal(now) {
		t.Fatalf("unexpected profile: %+v", profile.User)
	}
	if bytes.Contains(body, []byte("password_hash")) || bytes.Contains(body, []byte("PasswordHash")) || bytes.Contains(body, []byte(oldPassword)) {
		t.Fatal("profile leaked password material")
	}
	requestJSON(t, handler, http.MethodPut, "/api/v1/auth/password", login.AccessToken, map[string]string{"current_password": "wrong", "new_password": newPassword}, http.StatusBadRequest)
	requestJSON(t, handler, http.MethodGet, "/api/v1/auth/me", login.AccessToken, nil, http.StatusOK)
	changeBody := requestJSON(t, handler, http.MethodPut, "/api/v1/auth/password", login.AccessToken, map[string]string{"current_password": oldPassword, "new_password": newPassword}, http.StatusNoContent)
	if len(changeBody) != 0 {
		t.Fatalf("unexpected response body: %s", changeBody)
	}
	requestJSON(t, handler, http.MethodGet, "/api/v1/auth/me", login.AccessToken, nil, http.StatusUnauthorized)
	requestJSON(t, handler, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"username": "admin", "password": oldPassword}, http.StatusUnauthorized)
	decodeResponse(t, requestJSON(t, handler, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"username": "admin", "password": newPassword}, http.StatusOK), &login)
	requestJSON(t, handler, http.MethodGet, "/api/v1/auth/me", login.AccessToken, nil, http.StatusOK)
	stored, err := store.AdministratorByID(t.Context(), "adm_profile")
	if err != nil || bytes.Equal(stored.PasswordHash, []byte(newPassword)) {
		t.Fatal("password was not stored as a hash")
	}
	events := store.AuditEvents()
	encoded, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "auth.password.change") || bytes.Contains(encoded, []byte(newPassword)) {
		t.Fatal("password change audit missing or exposed secret")
	}
}
