package memoryrepo

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestRetireGroupMemberRemovesCandidateAndRecompilesBundle(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	store := New(auth.Administrator{ID: "admin", Username: "admin", CreatedAt: now})
	group := groups.DeviceGroup{ID: "group-entry", Name: "entry", Kind: groups.KindEntry, SelectionPolicy: endpoints.SelectionWeightedRoundRobin, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateDeviceGroup(ctx, group, projectionAudit("create", now)); err != nil {
		t.Fatal(err)
	}
	store.nodes["node-1"] = nodes.Node{ID: "node-1", Name: "node-1", CreatedAt: now, UpdatedAt: now}
	store.revisionsByGroup[group.ID] = []generations.GroupRevision{{ID: "revision-1", GroupID: group.ID, Revision: 1, Engine: agentv1.EngineGOST, Config: json.RawMessage(`{"services":[]}`), CreatedAt: now}}
	member := groups.Member{GroupID: group.ID, NodeID: "node-1", DialHost: "node-1.example.test", Weight: 100, CreatedAt: now, UpdatedAt: now}
	if _, assignments, err := store.UpsertGroupMember(ctx, member, projectionAudit("add", now)); err != nil || len(assignments) != 1 {
		t.Fatalf("add member: assignments=%d err=%v", len(assignments), err)
	}
	pool := createProjectedPool(t, store, "pool-1", "entry.example.test", group.ID, "pool-key", now)
	retiredAt := now.Add(time.Minute)
	auditBefore := len(store.AuditEvents())
	result, err := store.RetireGroupMember(ctx, group.ID, "node-1", retiredAt, projectionAudit("retire", retiredAt))
	if err != nil {
		t.Fatal(err)
	}
	if result.Replayed || result.Member.RetiredAt == nil || len(result.Assignments) != 1 {
		t.Fatalf("unexpected retirement result: %+v", result)
	}
	var bundle agentv1.ConfigurationBundle
	if err := json.Unmarshal(result.Assignments[0].Config, &bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Fragments) != 0 {
		t.Fatalf("retired member still has node configuration fragments: %+v", bundle.Fragments)
	}
	members, err := store.ListGroupMembers(ctx, group.ID)
	if err != nil || len(members) != 1 || members[0].RetiredAt == nil {
		t.Fatalf("membership history not preserved: %+v, %v", members, err)
	}
	poolMembers, err := store.EndpointPoolMembers(ctx, pool.ID)
	if err != nil || len(poolMembers) != 0 {
		t.Fatalf("retired candidate still projected: %+v, %v", poolMembers, err)
	}
	updatedPool, err := store.EndpointPool(ctx, pool.ID)
	if err != nil || updatedPool.MemberCount != 0 {
		t.Fatalf("pool member count was not updated: %+v, %v", updatedPool, err)
	}
	replayed, err := store.RetireGroupMember(ctx, group.ID, "node-1", retiredAt.Add(time.Minute), projectionAudit("duplicate-retire", retiredAt))
	if err != nil || !replayed.Replayed || len(replayed.Assignments) != 0 || len(store.AuditEvents()) != auditBefore+1 {
		t.Fatalf("retirement replay changed state: result=%+v err=%v", replayed, err)
	}
	if _, err := store.RetireGroupMember(ctx, group.ID, "missing", retiredAt, projectionAudit("missing", retiredAt)); !errors.Is(err, faults.ErrNotFound) {
		t.Fatalf("missing member: got %v", err)
	}
}

func TestUpdateGroupMemberWeightZeroConflictAndReplay(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	store := New(auth.Administrator{ID: "admin", Username: "admin", CreatedAt: now})
	group := groups.DeviceGroup{ID: "group-weight", Name: "weight", Kind: groups.KindEntry, SelectionPolicy: endpoints.SelectionWeightedRoundRobin, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateDeviceGroup(ctx, group, projectionAudit("create", now)); err != nil {
		t.Fatal(err)
	}
	store.nodes["node-1"] = nodes.Node{ID: "node-1", Name: "node-1", CreatedAt: now, UpdatedAt: now}
	initial := groups.Member{GroupID: group.ID, NodeID: "node-1", Weight: 10, CreatedAt: now, UpdatedAt: now}
	if _, _, err := store.UpsertGroupMember(ctx, initial, projectionAudit("member", now)); err != nil {
		t.Fatal(err)
	}
	pool := createProjectedPool(t, store, "pool-weight", "entry.example.test", group.ID, "pool-weight-key", now)
	input := groups.UpdateMemberWeightInput{GroupID: group.ID, NodeID: "node-1", Weight: 0, ExpectedUpdatedAt: now, UpdatedAt: now.Add(time.Second), IdempotencyKey: "weight-key", RequestSHA256: "weight-hash", UpdatedBy: "admin"}
	result, err := store.UpdateGroupMemberWeight(ctx, input, projectionAudit("weight", input.UpdatedAt))
	if err != nil || result.Replayed || result.Member.Weight != 0 {
		t.Fatalf("update: %+v, %v", result, err)
	}
	projected, err := store.EndpointPoolMembers(ctx, pool.ID)
	if err != nil || len(projected) != 1 || projected[0].Weight != 0 {
		t.Fatalf("projection: %+v, %v", projected, err)
	}
	replay, err := store.UpdateGroupMemberWeight(ctx, input, projectionAudit("weight-retry", input.UpdatedAt))
	if err != nil || !replay.Replayed || replay.Member.Weight != 0 {
		t.Fatalf("replay: %+v, %v", replay, err)
	}
	input.IdempotencyKey = "different-key"
	if _, err := store.UpdateGroupMemberWeight(ctx, input, projectionAudit("stale", input.UpdatedAt)); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("stale update: %v", err)
	}
	input.IdempotencyKey = "weight-key"
	input.RequestSHA256 = "different-hash"
	if _, err := store.UpdateGroupMemberWeight(ctx, input, projectionAudit("mismatch", input.UpdatedAt)); !errors.Is(err, faults.ErrIdempotencyConflict) {
		t.Fatalf("key reuse: %v", err)
	}
}

func TestRetireGroupMemberRestoresMembershipWhenBundleCompilationFails(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 11, 0, 0, 0, time.UTC)
	store := New(auth.Administrator{ID: "admin", Username: "admin", CreatedAt: now})
	for _, id := range []string{"group-1", "group-2"} {
		group := groups.DeviceGroup{ID: id, Name: id, Kind: groups.KindEntry, SelectionPolicy: endpoints.SelectionWeightedRoundRobin, CreatedAt: now, UpdatedAt: now}
		if err := store.CreateDeviceGroup(ctx, group, projectionAudit("create-"+id, now)); err != nil {
			t.Fatal(err)
		}
	}
	store.nodes["node-1"] = nodes.Node{ID: "node-1", Name: "node-1", CreatedAt: now, UpdatedAt: now}
	for _, id := range []string{"group-1", "group-2"} {
		member := groups.Member{GroupID: id, NodeID: "node-1", Weight: 100, CreatedAt: now, UpdatedAt: now}
		if _, _, err := store.UpsertGroupMember(ctx, member, projectionAudit("add-"+id, now)); err != nil {
			t.Fatal(err)
		}
	}
	pool := createProjectedPool(t, store, "pool-1", "entry.example.test", "group-1", "pool-key", now)
	store.revisionsByGroup["group-2"] = []generations.GroupRevision{{ID: "invalid-revision", GroupID: "group-2", Revision: 1, Engine: agentv1.EngineGOST, Config: json.RawMessage(`{broken`)}}
	before := len(store.AuditEvents())
	if _, err := store.RetireGroupMember(ctx, "group-1", "node-1", now.Add(time.Minute), projectionAudit("retire-failed", now)); err == nil {
		t.Fatal("expected bundle compilation failure")
	}
	members, err := store.ListGroupMembers(ctx, "group-1")
	if err != nil || len(members) != 1 || members[0].RetiredAt != nil || !members[0].UpdatedAt.Equal(now) {
		t.Fatalf("failed retirement changed source membership: %+v, %v", members, err)
	}
	poolMembers, err := store.EndpointPoolMembers(ctx, pool.ID)
	if err != nil || len(poolMembers) != 1 || len(store.AuditEvents()) != before {
		t.Fatalf("failed retirement changed projection or audit: %+v, %v", poolMembers, err)
	}
}
