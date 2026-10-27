package postgressnapshot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

func TestHotSnapshotRejectsUnreadableSuccessfulMutation(t *testing.T) {
	state, before := snapshotReadFixture(t, 1)
	if raw, err := encodeHotSnapshot(state); err != nil || !bytes.Equal(raw, before) {
		t.Fatal("valid persistence state was changed or rejected", err)
	}
	now := time.Now().UTC()
	// A missed domain check must remain local to this write. The repository
	// accepts this session, but its administrator reference cannot be restored.
	if err := state.CreateSession(context.Background(), auth.Session{ID: "invalid-session", AdminID: "missing-admin", TokenHash: fmt.Sprintf("%064x", 100), CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, audit.Event{}); err != nil {
		t.Fatal(err)
	}
	if raw, err := encodeHotSnapshot(state); err == nil || raw != nil {
		t.Fatal("an unreadable successful mutation reached persistence")
	}
}

func TestPostgreSQLUnreadableMutationLeavesCommittedStateUsable(t *testing.T) {
	ctx := context.Background()
	admin := bootstrapAdministrator(t)
	s, err := Open(ctx, isolatedDatabaseURL(t), admin)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var beforeRaw []byte
	var beforeRevision int64
	if err := s.db.QueryRowContext(ctx, `SELECT payload,revision FROM nyvp_control_snapshots WHERE singleton=true`).Scan(&beforeRaw, &beforeRevision); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	hash := fmt.Sprintf("%064x", 100)
	err = mutate(ctx, s, func(state *memoryrepo.Store) error {
		return state.CreateSession(ctx, auth.Session{ID: "must-not-commit", AdminID: "missing-admin", TokenHash: hash, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, audit.Event{ID: "must-not-commit"})
	})
	if err == nil {
		t.Fatal("unreadable state committed")
	}
	var afterRaw []byte
	var afterRevision int64
	if err := s.db.QueryRowContext(ctx, `SELECT payload,revision FROM nyvp_control_snapshots WHERE singleton=true`).Scan(&afterRaw, &afterRevision); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeRaw, afterRaw) || beforeRevision != afterRevision {
		t.Fatal("failed validation replaced the committed state")
	}
	if events, err := s.AuditEvents(ctx); err != nil || len(events) != 0 {
		t.Fatal("failed write leaked an audit journal entry", err)
	}
	if _, err := s.SessionByTokenHash(ctx, hash, now); !errors.Is(err, faults.ErrUnauthorized) {
		t.Fatal("failed write poisoned authentication or exposed its session", err)
	}
	if _, err := s.AdministratorByID(ctx, admin.ID); err != nil {
		t.Fatal("administrator access failed after the rejected write", err)
	}
	if err := s.CreateSession(ctx, auth.Session{ID: "valid-after-rejection", AdminID: admin.ID, TokenHash: hash, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, audit.Event{ID: "valid-after-rejection"}); err != nil {
		t.Fatal("a rejected mutation prevented later valid writes", err)
	}
}
