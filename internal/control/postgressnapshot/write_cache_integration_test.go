package postgressnapshot

import (
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

func TestCommittedWriteSeedsValidatedDetachedReadCache(t *testing.T) {
	dsn := isolatedDatabaseURL(t)
	ctx := context.Background()
	admin := bootstrapAdministrator(t)
	s, err := Open(ctx, dsn, admin)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	hash := fmt.Sprintf("%064x", 1)
	var privateState *memoryrepo.Store
	_, err = transact(ctx, s, true, func(state *memoryrepo.Store) (struct{}, error) {
		privateState = state
		return struct{}{}, state.CreateSession(ctx, auth.Session{ID: "committed-session", AdminID: admin.ID, TokenHash: hash, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, audit.Event{ID: "committed-login"})
	})
	if err != nil {
		t.Fatal(err)
	}
	cached := s.readCache
	if cached.usage == nil || cached.identities == nil || len(cached.payload) == 0 || cached.transactionID == "" || cached.revision <= 1 {
		t.Fatal("committed transaction did not seed read cache")
	}
	if _, err = s.SessionByTokenHash(ctx, hash, now); err != nil || s.readCache.identities != cached.identities || &s.readCache.payload[0] != &cached.payload[0] {
		t.Fatal("authentication re-decoded a just-committed snapshot", err)
	}
	// The transaction store remains private, including its history reader and
	// pending audits. Mutating it must not alter the published identity view.
	if err = privateState.UpdateAdministratorPassword(ctx, admin.ID, admin.PasswordHash, admin.PasswordHash, true, audit.Event{ID: "uncommitted-change"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SessionByTokenHash(ctx, hash, now); err != nil {
		t.Fatal("transaction store contaminated committed identities", err)
	}
	events, err := s.AuditEvents(ctx)
	if err != nil || len(events) != 1 || events[0].ID != "committed-login" {
		t.Fatal("cached write duplicated or leaked pending audit events", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SessionByTokenHash(ctx, hash, now); err == nil {
		t.Fatal("committed write cache bypassed unavailable database")
	}
}

func TestFailedWriteNeverPublishesReadCache(t *testing.T) {
	dsn := isolatedDatabaseURL(t)
	ctx := context.Background()
	admin := bootstrapAdministrator(t)
	s, err := Open(ctx, dsn, admin)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.AdministratorByID(ctx, admin.ID); err != nil {
		t.Fatal(err)
	}
	cached := s.readCache
	change := func(state *memoryrepo.Store) error {
		return state.UpdateAdministratorPassword(ctx, admin.ID, admin.PasswordHash, admin.PasswordHash, true, audit.Event{ID: "failed-password-change"})
	}
	t.Run("operation rollback", func(t *testing.T) {
		err = mutate(ctx, s, func(state *memoryrepo.Store) error {
			if err := change(state); err != nil {
				return err
			}
			return faults.ErrConflict
		})
		if !errors.Is(err, faults.ErrConflict) {
			t.Fatal(err)
		}
	})
	t.Run("deferred commit failure", func(t *testing.T) {
		// A deferred constraint fails after UPDATE RETURNING has succeeded.
		// This is isolated fault injection, never a production database trigger.
		_, err = s.db.ExecContext(ctx, `CREATE FUNCTION reject_snapshot_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'isolated commit rejection'; RETURN NEW; END $$;
CREATE CONSTRAINT TRIGGER reject_snapshot_commit AFTER UPDATE ON nyvp_control_snapshots DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_snapshot_commit()`)
		if err != nil {
			t.Fatal(err)
		}
		if err = mutate(ctx, s, change); err == nil {
			t.Fatal("deferred database failure was ignored")
		}
	})
	if s.readCache.usage != cached.usage || s.readCache.identities != cached.identities || &s.readCache.payload[0] != &cached.payload[0] {
		t.Fatal("failed transaction published uncommitted cache")
	}
	actual, err := s.AdministratorByID(ctx, admin.ID)
	if err != nil || actual.MustChangePassword {
		t.Fatal("uncommitted password state became visible", err)
	}
	events, err := s.AuditEvents(ctx)
	if err != nil || len(events) != 0 {
		t.Fatal("failed transaction persisted an audit", err)
	}
}
