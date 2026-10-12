package postgressnapshot

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

func TestPostgreSQLRetiredMemberDeletionIsDurableAndPreservesLiveMembership(t *testing.T) {
	dsn := isolatedDatabaseURL(t)
	ctx := context.Background()
	admin := bootstrapAdministrator(t)
	s, err := Open(ctx, dsn, admin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	now := time.Now().UTC()
	event := audit.Event{ID: "member-delete-setup", ActorType: "administrator", ActorID: admin.ID, Action: "test", Outcome: "succeeded", CreatedAt: now}
	if err := s.CreateEnrollmentToken(ctx, enrollment.Token{ID: "token", TokenHash: "token-hash", ExpiresAt: now.Add(time.Hour)}, event); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeEnrollmentToken(ctx, enrollment.ConsumeInput{TokenHash: "token-hash", CredentialHash: "credential-hash", Node: nodes.Node{ID: "node", Hostname: "isolated.example.test", CreatedAt: now, UpdatedAt: now}}, now, event); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"old", "new"} {
		if err := s.CreateDeviceGroup(ctx, groups.DeviceGroup{ID: id, Name: id, Kind: groups.KindEntry, SelectionPolicy: endpoints.SelectionWeightedRoundRobin, CreatedAt: now}, event); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.UpsertGroupMember(ctx, groups.Member{GroupID: id, NodeID: "node", Weight: 100, CreatedAt: now, UpdatedAt: now}, event); err != nil {
			t.Fatal(err)
		}
	}
	retired, err := s.RetireGroupMember(ctx, "old", "node", now.Add(time.Second), event)
	if err != nil {
		t.Fatal(err)
	}
	beforeNode, err := s.NodeByCredentialHash(ctx, "credential-hash")
	if err != nil {
		t.Fatal(err)
	}
	beforeNew, err := s.ListGroupMembers(ctx, "new")
	if err != nil {
		t.Fatal(err)
	}
	service := groups.NewService(s, func() time.Time { return now.Add(2 * time.Second) })
	result, err := service.DeleteRetiredMember(ctx, admin.ID, "old", "node", retired.Member.UpdatedAt, "delete-once")
	if err != nil || !result.Deleted || result.Replayed {
		t.Fatalf("delete: %+v %v", result, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(ctx, dsn, admin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	old, err := restored.ListGroupMembers(ctx, "old")
	if err != nil || len(old) != 0 {
		t.Fatal("deleted membership reappeared after restart")
	}
	afterNode, err := restored.NodeByCredentialHash(ctx, "credential-hash")
	if err != nil || !reflect.DeepEqual(beforeNode, afterNode) {
		t.Fatal("deletion changed node identity or configuration")
	}
	afterNew, err := restored.ListGroupMembers(ctx, "new")
	if err != nil || !reflect.DeepEqual(beforeNew, afterNew) {
		t.Fatal("other group membership changed")
	}
	// Re-create the old relationship; replay must not remove its new incarnation.
	if _, _, err := restored.UpsertGroupMember(ctx, groups.Member{GroupID: "old", NodeID: "node", Weight: 100, CreatedAt: now, UpdatedAt: now.Add(3 * time.Second)}, event); err != nil {
		t.Fatal(err)
	}
	service = groups.NewService(restored, func() time.Time { return now.Add(4 * time.Second) })
	result, err = service.DeleteRetiredMember(ctx, admin.ID, "old", "node", retired.Member.UpdatedAt, "delete-once")
	if err != nil || !result.Replayed {
		t.Fatalf("durable replay: %+v %v", result, err)
	}
	old, err = restored.ListGroupMembers(ctx, "old")
	if err != nil || len(old) != 1 || old[0].RetiredAt != nil {
		t.Fatal("replay removed reactivated membership")
	}
	if _, err := service.DeleteRetiredMember(ctx, admin.ID, "old", "node", old[0].UpdatedAt, "reject-active"); !errors.Is(err, faults.ErrConflict) {
		t.Fatal("active deletion accepted")
	}
	events, err := restored.AuditEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Action == "device_group.member_delete" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("delete audit count %d", count)
	}
}
