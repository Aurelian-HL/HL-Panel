package httpapi_test

import (
	"bytes"
	"context"
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
	"github.com/hongle/hl-panel/internal/control/nodeupdates"
	"github.com/hongle/hl-panel/internal/control/releases"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type nodeUpdateVersions struct{}

func (nodeUpdateVersions) Check(context.Context) releases.Status {
	return releases.Status{Current: "v0.1.123", Versions: []releases.Version{{Tag: "v0.1.123"}}}
}
func (nodeUpdateVersions) ResolveNodeArchive(context.Context, string, string) (string, error) {
	return strings.Repeat("a", 64), nil
}

func TestNodeUpdateHTTPAuthorizationIsolationAndDurableReplay(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	hash, err := auth.HashPassword("test-updater-password")
	if err != nil {
		t.Fatal(err)
	}
	store := memoryrepo.New(auth.Administrator{ID: "update-admin", Username: "admin", PasswordHash: hash, CreatedAt: now})
	handler := httpapi.New(auth.NewService(store, audit.NewService(store), time.Now, time.Hour), enrollment.NewService(store, time.Now, time.Hour), nodes.NewService(store, time.Now, time.Minute), groups.NewService(store, time.Now), endpoints.NewService(store, time.Now), generations.NewService(store, time.Now), slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.WithNodeUpdates(nodeupdates.New(store, nodeUpdateVersions{})))
	var login auth.LoginResult
	decodeResponse(t, requestJSON(t, handler, "POST", "/api/v1/auth/login", "", map[string]string{"username": "admin", "password": "test-updater-password"}, 200), &login)
	enroll := func(name string) agentv1.EnrollmentResponse {
		var token enrollment.IssueResult
		decodeResponse(t, requestJSON(t, handler, "POST", "/api/v1/enrollment-tokens", login.AccessToken, map[string]any{"name": name, "expires_in_seconds": 900}, 201), &token)
		var node agentv1.EnrollmentResponse
		decodeResponse(t, requestJSON(t, handler, "POST", "/api/v1/agent/enroll", "", agentv1.EnrollmentRequest{EnrollmentToken: token.Token, Hostname: name, Platform: "linux", Architecture: "amd64", AgentVersion: "v0.1.122"}, 201), &node)
		requestJSON(t, handler, "POST", "/api/v1/agent/heartbeat", node.NodeCredential, agentv1.HeartbeatRequest{BootID: "update-test-boot", Hostname: name, Platform: "linux", Architecture: "amd64", AgentVersion: "v0.1.122", Capabilities: []string{nodeupdates.Capability}}, 204)
		return node
	}
	one, two := enroll("one"), enroll("two")
	path := "/api/v1/nodes/" + one.NodeID + "/updates"
	requestJSON(t, handler, "GET", path, "", nil, 401)
	requestJSON(t, handler, "GET", path, one.NodeCredential, nil, 401)
	requestWithKey(t, handler, path, login.AccessToken, "", map[string]string{"version": "v0.1.123"}, 400)
	const key = "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"
	created := requestWithKey(t, handler, path, login.AccessToken, key, map[string]string{"version": "v0.1.123"}, 202)
	replay := requestWithKey(t, handler, path, login.AccessToken, key, map[string]string{"version": "v0.1.123"}, 202)
	if !bytes.Equal(created, replay) {
		t.Fatal("task replay changed")
	}
	requestWithKey(t, handler, "/api/v1/nodes/"+two.NodeID+"/updates", login.AccessToken, key, map[string]string{"version": "v0.1.123"}, 409)
	claimPath := "/api/v1/agent/update-task/claim"
	requestJSON(t, handler, "POST", claimPath, login.AccessToken, map[string]any{}, 401)
	requestJSON(t, handler, "POST", claimPath, two.NodeCredential, map[string]any{}, 204)
	var task agentv1.NodeUpdateTask
	decodeResponse(t, requestJSON(t, handler, "POST", claimPath, one.NodeCredential, map[string]any{}, 200), &task)
	public := requestJSON(t, handler, "GET", path, login.AccessToken, nil, 200)
	if bytes.Contains(public, []byte(task.Lease)) || bytes.Contains(public, []byte(one.NodeCredential)) {
		t.Fatal("public status leaked secret")
	}
	receipt := agentv1.NodeUpdateResult{ID: key, Lease: task.Lease, State: "running", Phase: "download"}
	requestJSON(t, handler, "POST", "/api/v1/agent/update-task/result", two.NodeCredential, receipt, 409)
	requestJSON(t, handler, "POST", "/api/v1/agent/update-task/result", one.NodeCredential, receipt, 204)
	requestJSON(t, handler, "POST", path+"/"+key+"/cancel", login.AccessToken, map[string]any{}, 409)
	saved, err := store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := memoryrepo.DecodeSnapshot(saved)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := restored.ClaimNodeUpdate(ctx, one.NodeID, strings.Repeat("c", 64), time.Now())
	if err != nil || claim == nil || claim.Lease != task.Lease {
		t.Fatal("lease changed after restart", err)
	}
	requestJSON(t, handler, http.MethodPost, "/api/v1/agent/update-task/result", one.NodeCredential, map[string]any{"id": key, "lease": task.Lease, "state": "running", "phase": "download", "command": "id"}, 400)
}
