package httpapi_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

func TestDeviceGroupDeleteHTTPContract(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	passwordHash, err := auth.HashPassword("group-delete-http-password")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	store := memoryrepo.New(auth.Administrator{
		ID:           "adm_group_delete_http",
		Username:     "admin",
		PasswordHash: passwordHash,
		CreatedAt:    now,
	})
	auditService := audit.NewService(store)
	handler := httpapi.New(
		auth.NewService(store, auditService, clock, time.Hour),
		enrollment.NewService(store, clock, time.Hour),
		nodes.NewService(store, clock, 90*time.Second),
		groups.NewService(store, clock),
		endpoints.NewService(store, clock),
		generations.NewService(store, clock),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	loginBody := requestJSON(t, handler, http.MethodPost, "/api/v1/auth/login", "", map[string]any{
		"username": "admin",
		"password": "group-delete-http-password",
	}, http.StatusOK)
	var login auth.LoginResult
	decodeResponse(t, loginBody, &login)
	if login.AccessToken == "" {
		t.Fatal("login did not return an access token")
	}

	referenced := createGroup(t, handler, login.AccessToken, "referenced-group", groups.KindEntry)
	poolBody := requestJSON(t, handler, http.MethodPost, "/api/v1/endpoint-pools", login.AccessToken, map[string]any{
		"name":             "referencing-pool",
		"group_id":         referenced.ID,
		"mode":             "SINGLE_SERVICE_ENDPOINT",
		"protocol":         "socks5",
		"hostname":         "edge.example.test",
		"port":             1080,
		"selection_policy": "weighted_round_robin",
		"idempotency_key":  "group-delete-pool-create",
	}, http.StatusCreated)
	if len(poolBody) == 0 {
		t.Fatal("endpoint pool creation returned an empty response")
	}

	deletePath := "/api/v1/device-groups/" + referenced.ID
	deleteGroup := func(bearer, key string, status int) []byte {
		t.Helper()
		request := newDeleteRequest(t, deletePath, bearer)
		if key != "" {
			request.Header.Set("Idempotency-Key", key)
		}
		return serveDelete(t, handler, request, status)
	}
	deleteGroup("", "delete-referenced", http.StatusUnauthorized)
	deleteGroup(login.AccessToken, "", http.StatusBadRequest)
	deleteGroup(login.AccessToken, "delete-referenced", http.StatusConflict)

	free := createGroup(t, handler, login.AccessToken, "free-group", groups.KindExit)
	freePath := "/api/v1/device-groups/" + free.ID
	firstBody := deletePathRequest(t, handler, freePath, login.AccessToken, "delete-free", http.StatusOK)
	var first struct {
		Replayed bool `json:"replayed"`
	}
	decodeResponse(t, firstBody, &first)
	if first.Replayed {
		t.Fatal("first device-group delete reported replay")
	}
	replayBody := deletePathRequest(t, handler, freePath, login.AccessToken, "delete-free", http.StatusOK)
	var replay struct {
		Replayed bool `json:"replayed"`
	}
	decodeResponse(t, replayBody, &replay)
	if !replay.Replayed {
		t.Fatal("device-group delete retry did not report replay")
	}
	deletePathRequest(t, handler, "/api/v1/device-groups/missing", login.AccessToken, "delete-missing", http.StatusNotFound)

	groupsBody := requestJSON(t, handler, http.MethodGet, "/api/v1/device-groups", login.AccessToken, nil, http.StatusOK)
	var inventory struct {
		Items []groups.DeviceGroup `json:"items"`
	}
	decodeResponse(t, groupsBody, &inventory)
	for _, item := range inventory.Items {
		if item.ID == free.ID {
			t.Fatalf("deleted device group remains in inventory: %+v", item)
		}
	}
	if _, err := store.ListDeviceGroups(context.Background()); err != nil {
		t.Fatalf("list device groups after HTTP delete: %v", err)
	}
}

func deletePathRequest(t *testing.T, handler http.Handler, path, bearer, key string, status int) []byte {
	t.Helper()
	request := newDeleteRequest(t, path, bearer)
	request.Header.Set("Idempotency-Key", key)
	return serveDelete(t, handler, request, status)
}

func newDeleteRequest(t *testing.T, path, bearer string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodDelete, path, nil)
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	return request
}

func serveDelete(t *testing.T, handler http.Handler, request *http.Request, status int) []byte {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != status {
		t.Fatalf("DELETE %s status = %d, want %d; body=%s", request.URL.Path, response.Code, status, response.Body.String())
	}
	return response.Body.Bytes()
}
