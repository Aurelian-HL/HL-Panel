package postgressnapshot

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customeridentity"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/faults"
)

func customerDeletionPGFixture(t *testing.T) (*Store, string, string, customers.Customer) {
	t.Helper()
	dsn := isolatedDatabaseURL(t)
	admin := bootstrapAdministrator(t)
	s, err := Open(context.Background(), dsn, admin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	customer, _, err := customers.NewService(s, nil).CreateCustomer(context.Background(), admin.ID, customers.CustomerInput{Username: "delete-test", Password: "disposable-password", IdempotencyKey: "create-delete-user"})
	if err != nil {
		t.Fatal(err)
	}
	return s, dsn, admin.ID, customer
}

func customerDeletionPGIdentity(t *testing.T, s *Store) *customeridentity.Service {
	t.Helper()
	service, err := customeridentity.NewService(s.CustomerIdentityRepository("test"), nil, customeridentity.Options{PasswordFingerprintKey: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestPostgreSQLCustomerDeletionSurvivesRestartAndReplays(t *testing.T) {
	s, dsn, adminID, customer := customerDeletionPGFixture(t)
	ctx := context.Background()
	identity := customerDeletionPGIdentity(t, s)
	var tokens []string
	for range 2 {
		login, err := identity.Login(ctx, customer.Username, "disposable-password")
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, login.AccessToken)
	}
	other, _, err := customers.NewService(s, nil).CreateCustomer(ctx, adminID, customers.CustomerInput{Username: "delete-survivor", Password: "disposable-password", IdempotencyKey: "create-delete-survivor"})
	if err != nil {
		t.Fatal(err)
	}
	otherLogin, err := identity.Login(ctx, other.Username, "disposable-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, dsn, auth.Administrator{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	for _, token := range tokens {
		if _, err := customerDeletionPGIdentity(t, reopened).Authenticate(ctx, token); err != nil {
			t.Fatal("restart lost a valid customer session", err)
		}
	}
	request := customers.DeleteRequest{Revision: customer.Revision}
	if replayed, err := customers.NewService(reopened, nil).DeleteCustomer(ctx, adminID, customer.ID, request, "delete-user"); err != nil || replayed {
		t.Fatal("durable deletion failed", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(ctx, dsn, auth.Administrator{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	if _, err := restarted.Customer(ctx, customer.ID); !errors.Is(err, faults.ErrNotFound) {
		t.Fatal("deleted customer returned after restart", err)
	}
	for _, token := range tokens {
		if _, err := customerDeletionPGIdentity(t, restarted).Authenticate(ctx, token); !errors.Is(err, faults.ErrUnauthorized) {
			t.Fatal("deleted account retained a durable session", err)
		}
	}
	if _, err := customerDeletionPGIdentity(t, restarted).Authenticate(ctx, otherLogin.AccessToken); err != nil {
		t.Fatal("another customer's session was removed", err)
	}
	before, _ := healthSnapshot(t, restarted)
	if replayed, err := customers.NewService(restarted, nil).DeleteCustomer(ctx, adminID, customer.ID, request, "delete-user"); err != nil || !replayed {
		t.Fatal("deletion replay after restart failed", err)
	}
	after, _ := healthSnapshot(t, restarted)
	if !bytes.Equal(before, after) {
		t.Fatal("deletion replay changed durable business state")
	}
}

func TestPostgreSQLCustomerDeletionRollsBackDeferredCommitFailure(t *testing.T) {
	s, _, adminID, customer := customerDeletionPGFixture(t)
	ctx := context.Background()
	identity := customerDeletionPGIdentity(t, s)
	login, err := identity.Login(ctx, customer.Username, "disposable-password")
	if err != nil {
		t.Fatal(err)
	}
	before, revision := healthSnapshot(t, s)
	cached := s.readCache
	_, err = s.db.ExecContext(ctx, `CREATE FUNCTION reject_customer_delete_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'isolated customer deletion commit rejection'; RETURN NEW; END $$;
CREATE CONSTRAINT TRIGGER reject_customer_delete_commit AFTER UPDATE ON nyvp_control_snapshots DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_customer_delete_commit()`)
	if err != nil {
		t.Fatal(err)
	}
	service := customers.NewService(s, nil)
	request := customers.DeleteRequest{Revision: customer.Revision}
	if _, err := service.DeleteCustomer(ctx, adminID, customer.ID, request, "retry-customer-delete"); err == nil {
		t.Fatal("deferred commit failure was ignored")
	}
	after, afterRevision := healthSnapshot(t, s)
	if revision != afterRevision || !bytes.Equal(before, after) || s.readCache.revision != cached.revision || s.readCache.identities != cached.identities || s.readCache.forwarding != cached.forwarding {
		t.Fatal("failed customer deletion published uncommitted state or cache")
	}
	if _, err := identity.Authenticate(ctx, login.AccessToken); err != nil {
		t.Fatal("failed deletion revoked the customer's session", err)
	}
	if _, err := s.db.ExecContext(ctx, `DROP TRIGGER reject_customer_delete_commit ON nyvp_control_snapshots; DROP FUNCTION reject_customer_delete_commit()`); err != nil {
		t.Fatal(err)
	}
	if replayed, err := service.DeleteCustomer(ctx, adminID, customer.ID, request, "retry-customer-delete"); err != nil || replayed {
		t.Fatal("rolled back customer deletion could not retry", err)
	}
}
