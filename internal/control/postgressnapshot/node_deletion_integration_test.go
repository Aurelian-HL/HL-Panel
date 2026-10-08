package postgressnapshot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

func TestPostgreSQLNodeDeletionSurvivesRestartAndRejectsHeartbeat(t *testing.T) {
	dsn := isolatedDatabaseURL(t)
	ctx := context.Background()
	admin := bootstrapAdministrator(t)
	s, err := Open(ctx, dsn, admin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	now := time.Now().UTC()
	event := audit.Event{ID: "node-delete-test", ActorType: "administrator", ActorID: admin.ID, Action: "node.delete", ResourceType: "node", ResourceID: "node", Outcome: "succeeded", CreatedAt: now, Metadata: map[string]any{}}
	if err := s.CreateEnrollmentToken(ctx, enrollment.Token{ID: "token", TokenHash: "token-hash", Name: "node", ExpiresAt: now.Add(time.Hour)}, event); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeEnrollmentToken(ctx, enrollment.ConsumeInput{TokenHash: "token-hash", CredentialHash: "node-hash", Node: nodes.Node{ID: "node", Hostname: "node", CreatedAt: now, UpdatedAt: now}}, now, event); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateHeartbeat(ctx, "node", nodes.Heartbeat{}, now, event); err != nil {
		t.Fatal(err)
	}
	input := nodes.DeleteInput{NodeID: "node", DeletedBy: admin.ID, IdempotencyKey: "delete", RequestSHA256: "delete-hash", DeletedAt: now, OnlineFor: time.Minute}
	if _, _, err := s.DeleteOffline(ctx, input, event); !errors.Is(err, faults.ErrConflict) {
		t.Fatal("online deletion accepted")
	}
	input.DeletedAt = now.Add(2 * time.Minute)
	if _, replayed, err := s.DeleteOffline(ctx, input, event); err != nil || replayed {
		t.Fatalf("offline deletion failed: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(ctx, dsn, admin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	list, err := restored.ListNodes(ctx)
	if err != nil || len(list) != 0 {
		t.Fatal("deleted node reappeared after restart")
	}
	tombstone, err := restored.NodeByCredentialHash(ctx, "node-hash")
	if err != nil || tombstone.DeletedAt == nil {
		t.Fatal("tombstone not persisted")
	}
	if _, replayed, err := restored.DeleteOffline(ctx, input, event); err != nil || !replayed {
		t.Fatal("deletion replay not durable")
	}
	if _, err := restored.UpdateHeartbeat(ctx, "node", nodes.Heartbeat{}, input.DeletedAt, event); !errors.Is(err, faults.ErrUnauthorized) {
		t.Fatal("heartbeat revived deleted identity")
	}
	overview, err := restored.Overview(ctx, input.DeletedAt, time.Minute)
	if err != nil || overview.NodeCount != 0 {
		t.Fatal("overview still counts deleted node")
	}
}
