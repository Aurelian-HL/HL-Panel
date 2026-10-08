package postgressnapshot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

func TestReadCacheTracksOtherWritersAndSameRevisionRepairs(t *testing.T) {
	dsn := isolatedDatabaseURL(t)
	ctx := context.Background()
	admin := bootstrapAdministrator(t)
	first, err := Open(ctx, dsn, admin)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(ctx, dsn, admin)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	now := time.Now().UTC()
	tokenHash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err = first.CreateSession(ctx, auth.Session{ID: "cache-session", AdminID: admin.ID, TokenHash: tokenHash, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, audit.Event{ID: "cache-login"}); err != nil {
		t.Fatal(err)
	}
	if _, err = first.SessionByTokenHash(ctx, tokenHash, now); err != nil {
		t.Fatal(err)
	}
	cached := first.readCache.state
	if _, err = first.SessionByTokenHash(ctx, tokenHash, now); err != nil || first.readCache.state != cached {
		t.Fatal("unchanged reads did not reuse validated state")
	}
	projection, err := first.AdministratorByID(ctx, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	projection.PasswordHash[0] ^= 1
	if err = second.UpdateAdministratorPassword(ctx, admin.ID, admin.PasswordHash, admin.PasswordHash, false, audit.Event{ID: "password-reset"}); err != nil {
		t.Fatal(err)
	}
	if _, err = first.SessionByTokenHash(ctx, tokenHash, now); !errors.Is(err, faults.ErrUnauthorized) {
		t.Fatal("cached session survived password reset in another instance")
	}
	cached = first.readCache.state
	err = mutate(ctx, second, func(state *memoryrepo.Store) error {
		_ = state.AppendAudit(ctx, audit.Event{ID: "rollback"})
		return faults.ErrConflict
	})
	if !errors.Is(err, faults.ErrConflict) {
		t.Fatal(err)
	}
	events, err := first.AuditEvents(ctx)
	if err != nil || len(events) != 2 || first.readCache.state != cached {
		t.Fatal("rollback invalidated or contaminated read state")
	}
	replacement, err := memoryrepo.New(admin).EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = second.db.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET payload=$1`, replacement); err != nil {
		t.Fatal(err)
	}
	events, err = first.AuditEvents(ctx)
	if err != nil || len(events) != 2 || first.readCache.state == cached {
		t.Fatal("same-revision replacement was hidden by cache")
	}
	if _, err = second.db.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET payload=$1`, []byte(`{"Version":17}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = first.ListNodes(ctx); err == nil {
		t.Fatal("same-revision corrupted snapshot was hidden by cache")
	}
	if err = second.Close(); err != nil {
		t.Fatal(err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = first.AdministratorByID(ctx, admin.ID); err == nil {
		t.Fatal("cached identity served while database unavailable")
	}
}
