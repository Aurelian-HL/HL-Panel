package postgressnapshot

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

func TestPostgreSQLServiceOwnerDeletionFormat25RestartAndCommitRollback(t *testing.T) {
	s, dsn, adminID, survivor := customerDeletionPGFixture(t)
	ctx := context.Background()
	identity := customerDeletionPGIdentity(t, s)
	login, err := identity.Login(ctx, survivor.Username, "disposable-password")
	if err != nil {
		t.Fatal(err)
	}
	service := customers.NewService(s, nil)
	owner, _, err := service.CreateCustomer(ctx, adminID, customers.CustomerInput{AccountKind: customers.AccountService, Username: "disposable-service-owner", IdempotencyKey: "create-service-owner"})
	if err != nil {
		t.Fatal(err)
	}
	before, revision := healthSnapshot(t, s)
	cached := s.readCache
	if _, err := s.db.ExecContext(ctx, `CREATE FUNCTION reject_service_delete_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'isolated service owner deletion commit rejection'; RETURN NEW; END $$;
CREATE CONSTRAINT TRIGGER reject_service_delete_commit AFTER UPDATE ON nyvp_control_snapshots DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_service_delete_commit()`); err != nil {
		t.Fatal(err)
	}
	request := customers.DeleteRequest{Revision: owner.Revision}
	if _, err := service.DeleteServiceOwner(ctx, adminID, owner.ID, request, "delete-service-owner"); err == nil {
		t.Fatal("deferred commit failure ignored")
	}
	after, afterRevision := healthSnapshot(t, s)
	if !bytes.Equal(before, after) || revision != afterRevision || s.readCache.revision != cached.revision || s.readCache.identities != cached.identities || s.readCache.forwarding != cached.forwarding {
		t.Fatal("failed commit published state, replay, audit or cache")
	}
	if _, err := s.db.ExecContext(ctx, `DROP TRIGGER reject_service_delete_commit ON nyvp_control_snapshots; DROP FUNCTION reject_service_delete_commit()`); err != nil {
		t.Fatal(err)
	}
	if replayed, err := service.DeleteServiceOwner(ctx, adminID, owner.ID, request, "delete-service-owner"); err != nil || replayed {
		t.Fatal("retry", replayed, err)
	}
	var version int
	if err := s.db.QueryRowContext(ctx, `SELECT format_version FROM nyvp_control_snapshots WHERE singleton=true`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 25 || memoryrepo.SnapshotVersion != 25 {
		t.Fatalf("format changed to %d", version)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, dsn, auth.Administrator{})
	if err != nil {
		t.Fatal("reopen format25", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if _, err := reopened.Customer(ctx, owner.ID); !errors.Is(err, faults.ErrNotFound) {
		t.Fatal("deleted owner restored", err)
	}
	if _, err := customerDeletionPGIdentity(t, reopened).Authenticate(ctx, login.AccessToken); err != nil {
		t.Fatal("survivor session lost", err)
	}
	beforeReplay, _ := healthSnapshot(t, reopened)
	if replayed, err := customers.NewService(reopened, nil).DeleteServiceOwner(ctx, adminID, owner.ID, request, "delete-service-owner"); err != nil || !replayed {
		t.Fatal("persisted replay", replayed, err)
	}
	afterReplay, _ := healthSnapshot(t, reopened)
	if !bytes.Equal(beforeReplay, afterReplay) {
		t.Fatal("persisted replay changed snapshot")
	}
}
