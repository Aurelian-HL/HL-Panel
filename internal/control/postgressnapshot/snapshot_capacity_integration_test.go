package postgressnapshot

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

func TestLegacySnapshotCapacityUpgradeRetainsLargeStateAndEnrollment(t *testing.T) {
	ctx, dsn, admin := context.Background(), isolatedDatabaseURL(t), bootstrapAdministrator(t)
	s, err := Open(ctx, dsn, admin)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		// Open returns nil on failure; keep the original assertion visible
		// instead of masking it with a second panic during cleanup.
		if s != nil {
			_ = s.Close()
		}
	}()
	// Reproduce an existing installation's constraint, not only a fresh schema.
	if _, err = s.db.Exec(`ALTER TABLE nyvp_control_snapshots DROP CONSTRAINT hl_panel_snapshot_payload_64m,
		ADD CONSTRAINT nyvp_control_snapshots_payload_check CHECK (octet_length(payload) BETWEEN 1 AND 16777216)`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = Open(ctx, dsn, admin)
	if err != nil {
		t.Fatal("legacy startup migration", err)
	}
	state, now := memoryrepo.New(admin), time.Now().UTC()
	for i := 0; i < 80000; i++ {
		hash := fmt.Sprintf("%064x", i+1)
		if err = state.CreateSession(ctx, auth.Session{ID: hash, AdminID: admin.ID, TokenHash: hash, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, audit.Event{}); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := encodeHotSnapshot(state)
	if err != nil || len(raw) <= 16<<20 || len(raw) > memoryrepo.MaxSnapshotBytes {
		t.Fatal("large hot snapshot fixture", len(raw), err)
	}
	if _, err = s.db.Exec(`UPDATE nyvp_control_snapshots SET payload=$1,revision=revision+1`, raw); err != nil {
		t.Fatal("database still has legacy bound", err)
	}
	if err = s.CreateEnrollmentToken(ctx, enrollment.Token{ID: "large-token", TokenHash: "large-token-hash", ExpiresAt: now.Add(time.Hour)}, audit.Event{}); err != nil {
		t.Fatal("large snapshot write", err)
	}
	if _, err = s.ConsumeEnrollmentToken(ctx, enrollment.ConsumeInput{TokenHash: "large-token-hash", CredentialHash: "large-node-credential", Node: nodes.Node{ID: "large-node", Hostname: "loopback", CreatedAt: now, UpdatedAt: now}}, now, audit.Event{}); err != nil {
		t.Fatal("large snapshot enrollment", err)
	}
	_ = s.Close()
	s, err = Open(ctx, dsn, admin)
	if err != nil {
		t.Fatal("large snapshot restart", err)
	}
	var size, constraints int
	if err = s.db.QueryRow(`SELECT octet_length(payload) FROM nyvp_control_snapshots`).Scan(&size); err != nil || size <= 16<<20 {
		t.Fatal("large state not retained", size, err)
	}
	if err = s.db.QueryRow(`SELECT count(*) FROM pg_constraint WHERE conrelid='nyvp_control_snapshots'::regclass AND conname='hl_panel_snapshot_payload_64m'`).Scan(&constraints); err != nil || constraints != 1 {
		t.Fatal("capacity migration is not idempotent", constraints, err)
	}
	t.Logf("legacy schema migrated; enrollment and restart persisted %d bytes", size)
}
