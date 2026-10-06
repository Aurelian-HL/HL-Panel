package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/httpapi"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

func TestCustomerRoutesUseDedicatedNamespaceAndSecret(t *testing.T) {
	store := memoryrepo.New(auth.Administrator{})
	if _, err := newCustomerAPIHandler(store, "test", []byte("too-short"), nil); err == nil {
		t.Fatal("short customer password fingerprint key accepted")
	}
	customerHandler, err := newCustomerAPIHandler(store, "20261003", []byte("0123456789abcdef0123456789abcdef"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	controlHandler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusTeapot)
	})
	handler := mountCustomerRoutes(controlHandler, customerHandler)

	portalRequest := httptest.NewRequest(http.MethodGet, "/api/v1/customer/portal", nil)
	portalResponse := httptest.NewRecorder()
	handler.ServeHTTP(portalResponse, portalRequest)
	if portalResponse.Code != http.StatusOK || !strings.Contains(portalResponse.Body.String(), `"panel_version":"20261003"`) {
		t.Fatalf("customer route was not mounted: status=%d body=%s", portalResponse.Code, portalResponse.Body.String())
	}
	controlRequest := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	controlResponse := httptest.NewRecorder()
	handler.ServeHTTP(controlResponse, controlRequest)
	if controlResponse.Code != http.StatusTeapot {
		t.Fatalf("non-customer route did not reach control handler: %d", controlResponse.Code)
	}
}

func TestCustomerAndAdministratorSessionsCannotCrossNamespaces(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC)
	adminHash, err := auth.HashPassword("administrator-password")
	if err != nil {
		t.Fatal(err)
	}
	store := memoryrepo.New(auth.Administrator{ID: "adm-cross", Username: "admin", PasswordHash: adminHash, CreatedAt: now})
	customerService := customers.NewService(store, func() time.Time { return now })
	group, _, err := customerService.CreateUserGroup(ctx, "adm-cross", customers.UserGroupInput{
		Name: "cross-boundary-users", IdempotencyKey: "cross-group-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := customerService.CreateCustomer(ctx, "adm-cross", customers.CustomerInput{
		Username: "cross-user", DisplayName: "Cross User", UserGroupID: group.ID,
		Password: "customer-password", IdempotencyKey: "cross-customer-1",
	}); err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return now }
	adminHandler := httpapi.New(
		auth.NewService(store, audit.NewService(store), clock, time.Hour),
		nil, nodes.NewService(store, clock, time.Minute), nil, nil, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	customerHandler, err := newCustomerAPIHandler(store, "test", []byte("0123456789abcdef0123456789abcdef"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	handler := mountCustomerRoutes(adminHandler, customerHandler)

	adminToken := loginToken(t, handler, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "administrator-password",
	})
	customerToken := loginToken(t, handler, "/api/v1/customer/auth/login", map[string]string{
		"username": "cross-user", "password": "customer-password",
	})

	// A customer bearer must never satisfy administrator middleware, even when
	// the requested resource is otherwise valid.
	if status := authorizedStatus(handler, http.MethodGet, "/api/v1/overview", customerToken); status != http.StatusUnauthorized {
		t.Fatalf("customer token reached administrator route: status=%d", status)
	}
	// An administrator bearer must never be accepted by the customer session
	// repository, even though both surfaces use the Authorization header.
	if status := authorizedStatus(handler, http.MethodGet, "/api/v1/customer/me", adminToken); status != http.StatusUnauthorized {
		t.Fatalf("administrator token reached customer route: status=%d", status)
	}
}

func loginToken(t *testing.T, handler http.Handler, path string, body map[string]string) string {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("login %s status=%d body=%s", path, response.Code, response.Body.String())
	}
	var result struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.AccessToken == "" {
		t.Fatalf("login %s returned an empty token", path)
	}
	return result.AccessToken
}

func authorizedStatus(handler http.Handler, method, path, token string) int {
	request := httptest.NewRequest(method, path, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response.Code
}
