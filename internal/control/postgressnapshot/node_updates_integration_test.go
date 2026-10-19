package postgressnapshot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/control/nodeupdates"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestNodeUpdateRestartKeepsLeaseAndNoopDoesNotRewriteSnapshot(t *testing.T) {
	dsn := isolatedDatabaseURL(t)
	ctx := context.Background()
	admin := bootstrapAdministrator(t)
	now := time.Now().UTC()
	s, err := Open(ctx, dsn, admin)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	event := audit.Event{ID: "update-fixture", ActorType: "administrator", ActorID: admin.ID, Action: "test", Outcome: "succeeded", CreatedAt: now, Metadata: map[string]any{}}
	if err = s.CreateEnrollmentToken(ctx, enrollment.Token{ID: "update-enroll", Name: "updater", TokenHash: "update-token-hash", ExpiresAt: now.Add(time.Hour)}, event); err != nil {
		t.Fatal(err)
	}
	_, err = s.ConsumeEnrollmentToken(ctx, enrollment.ConsumeInput{TokenHash: "update-token-hash", CredentialHash: "update-credential-hash", Node: nodes.Node{ID: "update-node", Hostname: "isolated.test", CreatedAt: now, UpdatedAt: now}}, now, event)
	if err != nil {
		t.Fatal(err)
	}
	heartbeat := nodes.Heartbeat{Hostname: "isolated.test", Platform: "linux", Architecture: "amd64", AgentVersion: "v0.1.122", Capabilities: []string{nodeupdates.Capability}}
	if _, err = s.UpdateHeartbeat(ctx, "update-node", heartbeat, now, event); err != nil {
		t.Fatal(err)
	}
	record := nodeupdates.Record{AdministratorID: admin.ID, Task: nodeupdates.Task{ID: "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa", NodeID: "update-node", TargetVersion: "v0.1.123", ArchiveSHA256: strings.Repeat("a", 64), State: "queued", Phase: "queued", CreatedAt: now, UpdatedAt: now}}
	event.ID = "update-requested"
	if _, err = s.CreateNodeUpdate(ctx, record, event); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimNodeUpdate(ctx, "update-node", strings.Repeat("b", 64), now.Add(time.Second))
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dsn, admin)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.ClaimNodeUpdate(ctx, "update-node", strings.Repeat("c", 64), now.Add(2*time.Second))
	if err != nil || again == nil || again.Lease != claim.Lease {
		t.Fatal("lease changed", err)
	}
	revision := s.readCache.revision
	for i := 0; i < 3; i++ {
		if _, err = s.ClaimNodeUpdate(ctx, "update-node", strings.Repeat("d", 64), now.Add(3*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if s.readCache.revision != revision {
		t.Fatal("claim replay rewrote snapshot")
	}
	in := agentv1.NodeUpdateResult{ID: record.Task.ID, Lease: claim.Lease, State: "running", Phase: "download"}
	if err = s.ReportNodeUpdate(ctx, "update-node", in, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	revision = s.readCache.revision
	if err = s.ReportNodeUpdate(ctx, "update-node", in, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if s.readCache.revision != revision {
		t.Fatal("identical progress rewrote snapshot")
	}
}
