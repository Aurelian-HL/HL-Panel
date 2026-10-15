package memoryrepo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

func TestCreateDeviceGroupIdempotentReplaysAndSurvivesSnapshot(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	hash, err := auth.HashPassword("snapshot-test-password")
	if err != nil {
		t.Fatal(err)
	}
	store := New(auth.Administrator{ID: "admin-1", Username: "admin", PasswordHash: hash, CreatedAt: now})
	input := groups.CreateDeviceGroupInput{
		Group:     groups.DeviceGroup{ID: "group-idempotent", Name: "entry", Kind: groups.KindEntry, CreatedAt: now, UpdatedAt: now},
		CreatedBy: "admin-1", IdempotencyKey: "create-key", RequestSHA256: "create-hash",
	}
	if _, replayed, err := store.CreateDeviceGroupIdempotent(ctx, input, projectionAudit("create", now)); err != nil || replayed {
		t.Fatalf("first create: replayed=%v err=%v", replayed, err)
	}
	audits := len(store.AuditEvents())
	got, replayed, err := store.CreateDeviceGroupIdempotent(ctx, input, projectionAudit("retry", now.Add(time.Minute)))
	if err != nil || !replayed || got.ID != input.Group.ID || len(store.AuditEvents()) != audits {
		t.Fatalf("create replay: group=%+v replayed=%v err=%v audits=%d", got, replayed, err, len(store.AuditEvents()))
	}
	input.RequestSHA256 = "different-hash"
	if _, _, err := store.CreateDeviceGroupIdempotent(ctx, input, projectionAudit("conflict", now)); !errors.Is(err, faults.ErrIdempotencyConflict) {
		t.Fatalf("create key reuse: %v", err)
	}
	raw, err := store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	input.RequestSHA256 = "create-hash"
	if _, replayed, err := restored.CreateDeviceGroupIdempotent(ctx, input, projectionAudit("restored-retry", now)); err != nil || !replayed {
		t.Fatalf("restored create replay: replayed=%v err=%v", replayed, err)
	}
}

func TestUpsertAndRetireDeviceGroupMemberIdempotency(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 8, 13, 0, 0, 0, time.UTC)
	store := New(auth.Administrator{ID: "admin-1", Username: "admin", CreatedAt: now})
	group := groups.DeviceGroup{ID: "group-members", Name: "members", Kind: groups.KindEntry, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateDeviceGroup(ctx, group, projectionAudit("create", now)); err != nil {
		t.Fatal(err)
	}
	store.nodes["node-1"] = nodes.Node{ID: "node-1", Name: "node-1", CreatedAt: now, UpdatedAt: now}
	memberInput := groups.UpsertGroupMemberInput{
		Member:    groups.Member{GroupID: group.ID, NodeID: "node-1", DialHost: "edge.example.test", Weight: 100, UpdatedAt: now},
		CreatedBy: "admin-1", IdempotencyKey: "member-key", RequestSHA256: "member-hash",
	}
	member, assignments, replayed, err := store.UpsertGroupMemberIdempotent(ctx, memberInput, projectionAudit("member", now))
	if err != nil || replayed || member.NodeID != "node-1" {
		t.Fatalf("first member upsert: member=%+v assignments=%d replayed=%v err=%v", member, len(assignments), replayed, err)
	}
	audits := len(store.AuditEvents())
	replayedMember, replayedAssignments, replayed, err := store.UpsertGroupMemberIdempotent(ctx, memberInput, projectionAudit("member-retry", now.Add(time.Minute)))
	if err != nil || !replayed || replayedMember != member || len(replayedAssignments) != len(assignments) || len(store.AuditEvents()) != audits {
		t.Fatalf("member replay: member=%+v assignments=%d replayed=%v err=%v audits=%d", replayedMember, len(replayedAssignments), replayed, err, len(store.AuditEvents()))
	}
	memberInput.RequestSHA256 = "different-hash"
	if _, _, _, err := store.UpsertGroupMemberIdempotent(ctx, memberInput, projectionAudit("member-conflict", now)); !errors.Is(err, faults.ErrIdempotencyConflict) {
		t.Fatalf("member key reuse: %v", err)
	}

	retireInput := groups.RetireGroupMemberInput{GroupID: group.ID, NodeID: "node-1", RetiredAt: now.Add(time.Minute), RetiredBy: "admin-1", IdempotencyKey: "retire-key", RequestSHA256: "retire-hash"}
	retired, err := store.RetireGroupMemberIdempotent(ctx, retireInput, projectionAudit("retire", retireInput.RetiredAt))
	if err != nil || retired.Replayed || retired.Member.RetiredAt == nil {
		t.Fatalf("first member retire: result=%+v err=%v", retired, err)
	}
	audits = len(store.AuditEvents())
	replayedRetire, err := store.RetireGroupMemberIdempotent(ctx, retireInput, projectionAudit("retire-retry", now.Add(2*time.Minute)))
	if err != nil || !replayedRetire.Member.RetiredAt.Equal(*retired.Member.RetiredAt) || len(store.AuditEvents()) != audits {
		t.Fatalf("retire replay: result=%+v err=%v audits=%d", replayedRetire, err, len(store.AuditEvents()))
	}
	retireInput.RequestSHA256 = "different-hash"
	if _, err := store.RetireGroupMemberIdempotent(ctx, retireInput, projectionAudit("retire-conflict", now)); !errors.Is(err, faults.ErrIdempotencyConflict) {
		t.Fatalf("retire key reuse: %v", err)
	}
}
