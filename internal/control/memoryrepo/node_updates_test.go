package memoryrepo

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/control/nodeupdates"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

const updateID = "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"

func updateFixture(t *testing.T) (*Store, nodeupdates.Record, time.Time) {
	t.Helper()
	s := snapshotFixture(t)
	now := time.Now().UTC()
	n := s.nodes["node-1"]
	n.Platform = "linux"
	n.Architecture = "amd64"
	n.AgentVersion = "v0.1.122"
	n.LastHeartbeatAt = &now
	n.Capabilities = []string{nodeupdates.Capability}
	s.nodes[n.ID] = n
	r := nodeupdates.Record{AdministratorID: "adm-1", Task: nodeupdates.Task{ID: updateID, NodeID: n.ID, TargetVersion: "v0.1.123", ArchiveSHA256: strings.Repeat("a", 64), State: "queued", Phase: "queued", CreatedAt: now, UpdatedAt: now}}
	return s, r, now
}
func TestNodeUpdateLeaseIdempotencyAndSnapshot(t *testing.T) {
	ctx := context.Background()
	s, r, now := updateFixture(t)
	if _, err := s.CreateNodeUpdate(ctx, r, audit.Event{ID: "update-created"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReplayNodeUpdate(ctx, "other-admin", r.Task.NodeID, updateID, r.Task.TargetVersion); !errors.Is(err, faults.ErrIdempotencyConflict) {
		t.Fatalf("wrong admin replay: %v", err)
	}
	claim, err := s.ClaimNodeUpdate(ctx, r.Task.NodeID, strings.Repeat("b", 64), now.Add(time.Second))
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	next, err := s.ClaimNodeUpdate(ctx, r.Task.NodeID, strings.Repeat("c", 64), now.Add(24*time.Hour))
	if err != nil || next.Lease != claim.Lease {
		t.Fatal("claimed lease was reassigned")
	}
	raw, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	saved := restored.nodeUpdates[updateID]
	if saved.AdministratorID != "adm-1" || saved.Lease != claim.Lease {
		t.Fatal("private task fields lost on restart")
	}
	public, _ := json.Marshal(saved.Task)
	if strings.Contains(string(public), saved.Lease) || strings.Contains(string(public), "adm-1") {
		t.Fatal("private task data exposed")
	}
	if err := restored.ReportNodeUpdate(ctx, "other-node", agentv1.NodeUpdateResult{ID: updateID, Lease: claim.Lease, State: "succeeded"}, now); !errors.Is(err, faults.ErrConflict) {
		t.Fatal("cross-node receipt accepted")
	}
}
func TestNodeUpdateSuccessRequiresFreshVersionConfigurationAndClaim(t *testing.T) {
	ctx := context.Background()
	s, r, now := updateFixture(t)
	s.nodeUpdates[updateID] = r
	claim, _ := s.ClaimNodeUpdate(ctx, "node-1", strings.Repeat("b", 64), now)
	in := agentv1.NodeUpdateResult{ID: updateID, Lease: claim.Lease, State: "succeeded", Phase: "verified", VerifiedGeneration: 1, VerifiedSHA256: strings.Repeat("d", 64)}
	if err := s.ReportNodeUpdate(ctx, "node-1", in, now); err != nil {
		t.Fatal(err)
	}
	if s.nodeUpdates[updateID].Task.State != "verifying" {
		t.Fatal("stale agent accepted")
	}
	n := s.nodes["node-1"]
	n.AgentVersion = "v0.1.123"
	fresh := now.Add(time.Second)
	n.LastHeartbeatAt = &fresh
	n.AppliedGeneration = 1
	s.nodes[n.ID] = n
	s.nodeConfigsByNode[n.ID][1] = generations.NodeConfigGeneration{ConfigSHA256: in.VerifiedSHA256}
	if err := s.ReportNodeUpdate(ctx, n.ID, in, fresh); err != nil {
		t.Fatal(err)
	}
	if s.nodeUpdates[updateID].Task.State != "succeeded" {
		t.Fatal("valid proof rejected")
	}
	in.State = "running"
	if err := s.ReportNodeUpdate(ctx, n.ID, in, fresh); !errors.Is(err, faults.ErrConflict) {
		t.Fatal("terminal receipt regressed")
	}
}
func TestNodeUpdateSerializesAllNodes(t *testing.T) {
	ctx := context.Background()
	s, r, now := updateFixture(t)
	s.nodeUpdates[updateID] = r
	s.nodes["node-2"] = nodes.Node{ID: "node-2", Platform: "linux", Architecture: "amd64", LastHeartbeatAt: &now, Capabilities: []string{nodeupdates.Capability}}
	r.Task.ID = "bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb"
	r.Task.NodeID = "node-2"
	if _, err := s.CreateNodeUpdate(ctx, r, audit.Event{}); !errors.Is(err, faults.ErrConflict) {
		t.Fatal("parallel node update allowed")
	}
}

func TestNodeUpdateCancellationAndAmbiguousReceiptLock(t *testing.T) {
	ctx := context.Background()
	s, r, now := updateFixture(t)
	s.nodeUpdates[updateID] = r
	if _, err := s.CancelNodeUpdate(ctx, "adm-1", "node-1", updateID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelNodeUpdate(ctx, "adm-1", "node-1", updateID, now); err != nil {
		t.Fatal("cancel replay", err)
	}
	s.nodeUpdates[updateID] = r
	claim, _ := s.ClaimNodeUpdate(ctx, "node-1", strings.Repeat("b", 64), now)
	if _, err := s.CancelNodeUpdate(ctx, "adm-1", "node-1", updateID, now); !errors.Is(err, faults.ErrConflict) {
		t.Fatal("claimed task cancelled")
	}
	in := agentv1.NodeUpdateResult{ID: updateID, Lease: claim.Lease, State: "uncertain", Phase: "interrupted"}
	if err := s.ReportNodeUpdate(ctx, "node-1", in, now); err != nil {
		t.Fatal(err)
	}
	in.State = "failed"
	in.Phase = "update_failed"
	if err := s.ReportNodeUpdate(ctx, "node-1", in, now); !errors.Is(err, faults.ErrConflict) {
		t.Fatal("uncertain task unlocked as failed")
	}
	in.State = "running"
	in.Phase = "download"
	if err := s.ReportNodeUpdate(ctx, "node-1", in, now); !errors.Is(err, faults.ErrConflict) {
		t.Fatal("uncertain task restarted")
	}
	in.State = "rolled_back"
	in.Phase = "rollback"
	in.BackupDirectory = "/var/backups/hl-panel-node/update-v0.1.123-20261019T000000Z-abc123"
	if err := s.ReportNodeUpdate(ctx, "node-1", in, now); err != nil {
		t.Fatal("rollback report", err)
	}
}
