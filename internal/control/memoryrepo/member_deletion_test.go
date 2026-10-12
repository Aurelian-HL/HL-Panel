package memoryrepo

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

func TestDeleteRetiredMemberPreservesNodeAndOtherGroup(t *testing.T) {
	ctx := context.Background()
	hash, err := auth.HashPassword("isolated-member-delete-test")
	if err != nil {
		t.Fatal(err)
	}
	s := New(auth.Administrator{ID: "admin", Username: "admin", PasswordHash: hash})
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	retired := groups.Member{GroupID: "old", NodeID: "node", RetiredAt: &now, UpdatedAt: now}
	s.deviceGroups["old"] = groups.DeviceGroup{ID: "old"}
	s.membersByGroup["old"] = map[string]groups.Member{"node": retired}
	s.revisionByIdempotency["old"] = map[string]string{}
	s.deviceGroups["new"] = groups.DeviceGroup{ID: "new"}
	active := groups.Member{GroupID: "new", NodeID: "node", Weight: 100, UpdatedAt: now}
	s.membersByGroup["new"] = map[string]groups.Member{"node": active}
	s.revisionByIdempotency["new"] = map[string]string{}
	node := nodes.Node{ID: "node", CredentialHash: "node-credential", DesiredGeneration: 5, AppliedGeneration: 5, LastApplyStatus: "succeeded"}
	s.nodes["node"] = node
	s.nodesByCredential[node.CredentialHash] = node.ID
	oldPool := endpoints.EndpointPool{ID: "old-pool", GroupID: "old", MemberCount: 1}
	newPool := endpoints.EndpointPool{ID: "new-pool", GroupID: "new", MemberCount: 1}
	s.endpointPools[oldPool.ID], s.endpointPools[newPool.ID] = oldPool, newPool
	projected := endpoints.EndpointPoolMember{PoolID: newPool.ID, GroupID: "new", NodeID: "node", Weight: 100}
	s.endpointMembers[oldPool.ID] = map[string]endpoints.EndpointPoolMember{"node": {PoolID: oldPool.ID, GroupID: "old", NodeID: "node"}}
	s.endpointMembers[newPool.ID] = map[string]endpoints.EndpointPoolMember{"node": projected}
	service := groups.NewService(s, func() time.Time { return now.Add(time.Minute) })
	result, err := service.DeleteRetiredMember(ctx, "admin", "old", "node", now, "delete-once")
	if err != nil || !result.Deleted || result.Replayed {
		t.Fatalf("delete failed: %+v %v", result, err)
	}
	if len(s.membersByGroup["old"]) != 0 || len(s.endpointMembers[oldPool.ID]) != 0 || s.endpointPools[oldPool.ID].MemberCount != 0 {
		t.Fatal("retired membership or stale projection remained")
	}
	if !reflect.DeepEqual(s.nodes["node"], node) || !reflect.DeepEqual(s.membersByGroup["new"]["node"], active) || !reflect.DeepEqual(s.endpointMembers[newPool.ID]["node"], projected) || s.endpointPools[newPool.ID] != newPool {
		t.Fatal("deletion changed the live node or its other group")
	}
	if len(s.auditEvents) != 1 || s.auditEvents[0].Action != "device_group.member_delete" {
		t.Fatal("delete audit missing")
	}
	// A retry remains durable after snapshot restore and cannot delete a new membership.
	snapshot, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	restored.membersByGroup["old"]["node"] = groups.Member{GroupID: "old", NodeID: "node", UpdatedAt: now.Add(time.Minute)}
	service = groups.NewService(restored, func() time.Time { return now.Add(2 * time.Minute) })
	replay, err := service.DeleteRetiredMember(ctx, "admin", "old", "node", now, "delete-once")
	if err != nil || !replay.Replayed || len(restored.auditEvents) != 1 || restored.membersByGroup["old"]["node"].RetiredAt != nil {
		t.Fatalf("retry was not safe/durable: %+v %v", replay, err)
	}
	if _, err := service.DeleteRetiredMember(ctx, "admin", "old", "node", now.Add(time.Minute), "delete-once"); !errors.Is(err, faults.ErrIdempotencyConflict) {
		t.Fatalf("changed idempotency payload accepted: %v", err)
	}
}

func TestDeleteRetiredMemberRejectsActiveChangedAndInvalidRequests(t *testing.T) {
	now := time.Now().UTC()
	s := New(auth.Administrator{ID: "admin"})
	s.deviceGroups["group"] = groups.DeviceGroup{ID: "group"}
	s.membersByGroup["group"] = map[string]groups.Member{"node": {GroupID: "group", NodeID: "node", UpdatedAt: now}}
	service := groups.NewService(s, nil)
	ctx := context.Background()
	if _, err := service.DeleteRetiredMember(ctx, "admin", "group", "node", now, "active"); !errors.Is(err, faults.ErrConflict) {
		t.Fatal(err)
	}
	m := s.membersByGroup["group"]["node"]
	m.RetiredAt = &now
	m.UpdatedAt = now.Add(time.Second)
	s.membersByGroup["group"]["node"] = m
	if _, err := service.DeleteRetiredMember(ctx, "admin", "group", "node", now, "stale"); !errors.Is(err, faults.ErrConflict) {
		t.Fatal(err)
	}
	for _, key := range []string{"", "invalid key"} {
		if _, err := service.DeleteRetiredMember(ctx, "admin", "group", "node", m.UpdatedAt, key); !errors.Is(err, faults.ErrValidation) {
			t.Fatal(err)
		}
	}
	if _, err := service.DeleteRetiredMember(ctx, "admin", "group", "node", time.Time{}, "missing-time"); !errors.Is(err, faults.ErrValidation) {
		t.Fatal(err)
	}
	if _, err := service.DeleteRetiredMember(ctx, "admin", "group", "missing", now, "missing"); !errors.Is(err, faults.ErrNotFound) {
		t.Fatal(err)
	}
	if len(s.auditEvents) != 0 || len(s.membersByGroup["group"]) != 1 {
		t.Fatal("rejected deletion mutated state")
	}
	// Invalid repository input cannot bypass service validation.
	if _, err := s.DeleteRetiredGroupMember(ctx, groups.DeleteMemberInput{}, audit.Event{}); !errors.Is(err, faults.ErrValidation) {
		t.Fatal(err)
	}
}
