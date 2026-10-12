package httpapi_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestRetiredMemberDeletionHTTPContract(t *testing.T) {
	f := newBusinessFixture(t)
	var issued enrollment.IssueResult
	decodeResponse(t, requestJSON(t, f.handler, http.MethodPost, "/api/v1/enrollment-tokens", f.token, map[string]any{
		"name": "member-delete", "group_id": f.entry.ID, "expires_in_seconds": 900,
	}, http.StatusCreated), &issued)
	var enrolled agentv1.EnrollmentResponse
	decodeResponse(t, requestJSON(t, f.handler, http.MethodPost, "/api/v1/agent/enroll", "", agentv1.EnrollmentRequest{
		EnrollmentToken: issued.Token, Hostname: "isolated.example.test", Platform: "linux", Architecture: "amd64", AgentVersion: "test",
	}, http.StatusCreated), &enrolled)
	path := "/api/v1/device-groups/" + f.entry.ID + "/members/" + enrolled.NodeID
	ctx := context.Background()
	members, err := f.store.ListGroupMembers(ctx, f.entry.ID)
	if err != nil || len(members) != 1 {
		t.Fatalf("initial membership: %v %v", members, err)
	}
	body := map[string]any{"updated_at": members[0].UpdatedAt}
	requestJSON(t, f.handler, http.MethodDelete, path, "", body, http.StatusUnauthorized)
	nodeCaller := f
	nodeCaller.token = enrolled.NodeCredential
	businessRequest(t, nodeCaller, http.MethodDelete, path, "node-caller", body, http.StatusUnauthorized)
	businessRequest(t, f, http.MethodDelete, path, "active", body, http.StatusConflict)
	var retired groups.RetireMemberResult
	decodeResponse(t, businessRequest(t, f, http.MethodPost, path+"/retire", "retire", nil, http.StatusOK), &retired)
	body["updated_at"] = retired.Member.UpdatedAt
	businessRequest(t, f, http.MethodDelete, path, "", body, http.StatusBadRequest)
	businessRequest(t, f, http.MethodDelete, path, "unknown", map[string]any{"updated_at": retired.Member.UpdatedAt, "extra": true}, http.StatusBadRequest)
	businessRequest(t, f, http.MethodDelete, path, "missing-time", map[string]any{}, http.StatusBadRequest)
	businessRequest(t, f, http.MethodDelete, path, "bad-time", map[string]any{"updated_at": "bad"}, http.StatusBadRequest)
	businessRequest(t, f, http.MethodDelete, path, "stale", map[string]any{"updated_at": retired.Member.UpdatedAt.Add(-time.Second)}, http.StatusConflict)
	var result groups.DeleteMemberResult
	decodeResponse(t, businessRequest(t, f, http.MethodDelete, path, "delete", body, http.StatusOK), &result)
	if !result.Deleted || result.Replayed || result.GroupID != f.entry.ID || result.NodeID != enrolled.NodeID {
		t.Fatalf("delete result: %+v", result)
	}
	decodeResponse(t, businessRequest(t, f, http.MethodDelete, path, "delete", body, http.StatusOK), &result)
	if !result.Replayed {
		t.Fatal("repeat request was not replayed")
	}
	businessRequest(t, f, http.MethodDelete, path, "new-delete", body, http.StatusNotFound)
	members, err = f.store.ListGroupMembers(ctx, f.entry.ID)
	if err != nil || len(members) != 0 {
		t.Fatal("deleted membership still listed")
	}
	nodes, err := f.store.ListNodes(ctx)
	if err != nil || len(nodes) != 1 || nodes[0].ID != enrolled.NodeID {
		t.Fatal("membership deletion removed node identity")
	}
	events := f.store.AuditEvents()
	count := 0
	for _, event := range events {
		if event.Action == "device_group.member_delete" && event.Outcome == "succeeded" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("successful delete audit count %d", count)
	}
}
