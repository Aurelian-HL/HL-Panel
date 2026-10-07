package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/hongle/hl-panel/internal/control/nezhamonitor"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestHeartbeatAddressSurvivesPersistenceAndAppearsInMonitoring(t *testing.T) {
	now := time.Now().UTC()
	clock := func() time.Time { return now }
	hash, err := auth.HashPassword("probe-address-password")
	if err != nil {
		t.Fatal(err)
	}
	store := memoryrepo.New(auth.Administrator{ID: "adm_address", Username: "admin", PasswordHash: hash, CreatedAt: now})
	enroll := enrollment.NewService(store, clock, time.Hour)
	issued, err := enroll.Issue(context.Background(), "adm_address", "probe", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	node, err := enroll.Enroll(context.Background(), enrollment.EnrollInput{RawToken: issued.Token, Hostname: "probe", Platform: "linux", Architecture: "amd64", AgentVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	makeHandler := func(repository *memoryrepo.Store) http.Handler {
		return httpapi.New(auth.NewService(repository, audit.NewService(repository), clock, time.Hour), enrollment.NewService(repository, clock, time.Hour),
			nodes.NewService(repository, clock, 90*time.Second), groups.NewService(repository, clock), endpoints.NewService(repository, clock), generations.NewService(repository, clock),
			slog.New(slog.NewTextHandler(io.Discard, nil)))
	}
	handler := makeHandler(store)
	raw, _ := json.Marshal(agentv1.HeartbeatRequest{BootID: "boot", Hostname: "probe", Platform: "linux", Architecture: "amd64"})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/heartbeat", bytes.NewReader(raw))
	request.RemoteAddr = "127.0.0.1:50000"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+node.NodeCredential)
	request.Header.Set("X-Real-IP", "8.8.8.8")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("heartbeat failed: %d %s", response.Code, response.Body.String())
	}
	snapshot, err := store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	store, err = memoryrepo.DecodeSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	handler = makeHandler(store)
	loginBody := requestJSON(t, handler, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"username": "admin", "password": "probe-address-password"}, http.StatusOK)
	var login auth.LoginResult
	decodeResponse(t, loginBody, &login)
	body := requestJSON(t, handler, http.MethodGet, "/api/v1/monitoring/nezha/servers", login.AccessToken, nil, http.StatusOK)
	var result struct {
		Items []nezhamonitor.Item `json:"items"`
	}
	decodeResponse(t, body, &result)
	if len(result.Items) != 1 || result.Items[0].IPv4 != "8.8.8.8" || result.Items[0].Source != "hl" || result.Items[0].CPUPercent != nil {
		t.Fatalf("observed address was lost or measurements fabricated: %+v", result.Items)
	}
	if bytes.Contains(body, []byte(node.NodeCredential)) {
		t.Fatal("monitoring leaked node credential")
	}
}
