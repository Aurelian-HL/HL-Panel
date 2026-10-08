package httpapi_test

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestOfflineNodeDeletionAuthorizationDurabilityAndReinstall(t *testing.T) {
	f := newBusinessFixture(t)
	ctx := context.Background()
	issue := func(group string) enrollment.IssueResult {
		t.Helper()
		var token enrollment.IssueResult
		decodeResponse(t, requestJSON(t, f.handler, http.MethodPost, "/api/v1/enrollment-tokens", f.token,
			map[string]any{"name": "cleanup-test", "group_id": group, "expires_in_seconds": 900}, http.StatusCreated), &token)
		return token
	}
	token := issue(f.entry.ID)
	input := agentv1.EnrollmentRequest{EnrollmentToken: token.Token, Hostname: "cleanup.example.test", DialHost: "192.0.2.43", Platform: "linux", Architecture: "amd64", AgentVersion: "test"}
	var node agentv1.EnrollmentResponse
	decodeResponse(t, requestJSON(t, f.handler, http.MethodPost, "/api/v1/agent/enroll", "", input, http.StatusCreated), &node)
	route := "/api/v1/nodes/" + node.NodeID
	deletePathRequest(t, f.handler, route, "", "delete-test", http.StatusUnauthorized)
	deletePathRequest(t, f.handler, route, node.NodeCredential, "delete-test", http.StatusUnauthorized)
	deletePathRequest(t, f.handler, route, f.token, "", http.StatusBadRequest)
	deletePathRequest(t, f.handler, "/api/v1/nodes/nonexistent", f.token, "missing", http.StatusNotFound)
	_, err := f.store.UpdateHeartbeat(ctx, node.NodeID, nodes.Heartbeat{Hostname: input.Hostname}, time.Now().UTC(), audit.Event{})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := f.store.EncodeSnapshot()
	deletePathRequest(t, f.handler, route, f.token, "delete-test", http.StatusConflict)
	after, _ := f.store.EncodeSnapshot()
	if !bytes.Equal(before, after) {
		t.Fatal("online rejection mutated state")
	}
	_, err = f.store.UpdateHeartbeat(ctx, node.NodeID, nodes.Heartbeat{Hostname: input.Hostname}, time.Now().Add(-2*time.Minute).UTC(), audit.Event{})
	if err != nil {
		t.Fatal(err)
	}
	var first struct{ Replayed bool }
	decodeResponse(t, deletePathRequest(t, f.handler, route, f.token, "delete-test", http.StatusOK), &first)
	if first.Replayed {
		t.Fatal("first deletion replayed")
	}
	count := len(f.store.AuditEvents())
	decodeResponse(t, deletePathRequest(t, f.handler, route, f.token, "delete-test", http.StatusOK), &first)
	if !first.Replayed || len(f.store.AuditEvents()) != count {
		t.Fatal("deletion replay duplicated audit")
	}
	list, _ := f.store.ListNodes(ctx)
	overview, _ := f.store.Overview(ctx, time.Now(), time.Minute)
	members, _ := f.store.ListGroupMembers(ctx, f.entry.ID)
	if len(list) != 0 || overview.NodeCount != 0 || len(members) != 1 || members[0].RetiredAt == nil {
		t.Fatal("deleted node still active")
	}
	events := f.store.AuditEvents()
	if events[len(events)-1].Action != "node.delete" {
		t.Fatal("missing deletion audit")
	}
	monitoring := requestJSON(t, f.handler, http.MethodGet, "/api/v1/monitoring/nezha/servers", f.token, nil, http.StatusOK)
	if bytes.Contains(monitoring, []byte(node.NodeID)) {
		t.Fatal("deleted node still in probe inventory")
	}
	for _, path := range []string{"/api/v1/agent/desired", "/api/v1/agent/control"} {
		requestJSON(t, f.handler, http.MethodGet, path, node.NodeCredential, nil, http.StatusUnauthorized)
	}
	requestJSON(t, f.handler, http.MethodPost, "/api/v1/agent/heartbeat", node.NodeCredential, map[string]any{}, http.StatusUnauthorized)
	requestJSON(t, f.handler, http.MethodPost, "/api/v1/agent/reenroll", node.NodeCredential, input, http.StatusUnauthorized)
	raw, err := f.store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := memoryrepo.DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	service := nodes.NewService(restored, time.Now, time.Minute)
	if _, err := service.AuthenticateCredential(ctx, node.NodeCredential); err == nil {
		t.Fatal("deleted credential revived after restart")
	}
	if _, err := service.RecordHeartbeat(ctx, node.NodeID, nodes.Heartbeat{Hostname: input.Hostname, Platform: "linux", Architecture: "amd64", BootID: "boot"}); err == nil {
		t.Fatal("racing heartbeat revived tombstone")
	}
	fresh := issue(f.exit.ID)
	input.EnrollmentToken = fresh.Token
	body := requestJSON(t, f.handler, http.MethodPost, "/api/v1/agent/reenroll", node.NodeCredential, input, http.StatusOK)
	if !bytes.Contains(body, []byte(node.NodeID)) {
		t.Fatal("reinstall changed identity")
	}
	list, _ = f.store.ListNodes(ctx)
	members, _ = f.store.ListGroupMembers(ctx, f.exit.ID)
	if len(list) != 1 || list[0].DeletedAt != nil || list[0].LastHeartbeatAt != nil || len(members) != 1 || members[0].RetiredAt != nil {
		t.Fatal("fresh reinstall did not restore identity in latest group")
	}
	requestJSON(t, f.handler, http.MethodPost, "/api/v1/agent/reenroll", node.NodeCredential, input, http.StatusOK)
}

func TestUngroupedNeverSampledNodeCanBeDeleted(t *testing.T) {
	f := newBusinessFixture(t)
	var token enrollment.IssueResult
	decodeResponse(t, requestJSON(t, f.handler, http.MethodPost, "/api/v1/enrollment-tokens", f.token,
		map[string]any{"name": "ungrouped", "expires_in_seconds": 900}, http.StatusCreated), &token)
	var node agentv1.EnrollmentResponse
	decodeResponse(t, requestJSON(t, f.handler, http.MethodPost, "/api/v1/agent/enroll", "", agentv1.EnrollmentRequest{EnrollmentToken: token.Token, Hostname: "ungrouped", Platform: "linux", Architecture: "amd64", AgentVersion: "test"}, http.StatusCreated), &node)
	deletePathRequest(t, f.handler, "/api/v1/nodes/"+node.NodeID, f.token, "ungrouped-delete", http.StatusOK)
	list, _ := f.store.ListNodes(context.Background())
	if len(list) != 0 {
		t.Fatal("ungrouped node not removed")
	}
}
