package httpapi_test

import (
	"bytes"
	"encoding/json"
	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/httpapi"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/control/panelruntime"
	"log/slog"
	"net/http"
	"testing"
	"time"
)

func TestPanelLogAPIAdminPermissionLimitAndOperationRecords(t *testing.T) {
	hash, err := auth.HashPassword("isolated-log-admin")
	if err != nil {
		t.Fatal(err)
	}
	store := memoryrepo.New(auth.Administrator{ID: "admin", Username: "admin", PasswordHash: hash, CreatedAt: time.Now()})
	logs := panelruntime.NewLogStore("")
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	handler := httpapi.New(auth.NewService(store, audit.NewService(store), time.Now, time.Hour),
		enrollment.NewService(store, time.Now, time.Hour), nodes.NewService(store, time.Now, time.Minute), groups.NewService(store, time.Now),
		endpoints.NewService(store, time.Now), generations.NewService(store, time.Now), logger,
		httpapi.WithPanelRuntime(panelruntime.NewService("v0.1.44", nil, logger, time.Now).WithLogs(logs)))
	requestJSON(t, handler, http.MethodGet, "/api/v1/panel/logs", "", "", http.StatusUnauthorized)
	var login auth.LoginResult
	decodeResponse(t, requestJSON(t, handler, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "isolated-log-admin"}, http.StatusOK), &login)
	logger.Info("面板已启动", "credential", "private-secret")
	requestJSON(t, handler, http.MethodPost, "/api/v1/panel/control", login.AccessToken, map[string]any{"command": "version"}, http.StatusOK)
	body := requestJSON(t, handler, http.MethodGet, "/api/v1/panel/logs?limit=1", login.AccessToken, nil, http.StatusOK)
	var result panelruntime.LogResult
	if err = json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || !bytes.Contains(body, []byte("管理员操作请求")) || bytes.Contains(body, []byte("private-secret")) {
		t.Fatalf("invalid log result %s", body)
	}
	for _, limit := range []string{"0", "2001", "abc", "-1"} {
		requestJSON(t, handler, http.MethodGet, "/api/v1/panel/logs?limit="+limit, login.AccessToken, nil, http.StatusBadRequest)
	}
}
