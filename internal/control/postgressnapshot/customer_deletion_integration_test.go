package postgressnapshot

import (
	"context"
	"errors"
	"testing"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/faults"
)

func TestPostgreSQLCustomerDeletionSurvivesRestartAndReplays(t *testing.T) {
	dsn := isolatedDatabaseURL(t)
	ctx := context.Background()
	admin := bootstrapAdministrator(t)
	s, err := Open(ctx, dsn, admin)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	svc := customers.NewService(s, nil)
	group, _, err := svc.CreateUserGroup(ctx, admin.ID, customers.UserGroupInput{Name: "Delete test", IdempotencyKey: "create-delete-group"})
	if err != nil {
		t.Fatal(err)
	}
	customer, _, err := svc.CreateCustomer(ctx, admin.ID, customers.CustomerInput{Username: "delete-test", Password: "disposable-password", UserGroupID: group.ID, IdempotencyKey: "create-delete-user"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DeleteUserGroup(ctx, admin.ID, group.ID, customers.DeleteRequest{Revision: group.Revision}, "delete-group"); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("group reference: %v", err)
	}
	if _, err := svc.DeleteCustomer(ctx, admin.ID, customer.ID, customers.DeleteRequest{Revision: customer.Revision}, "delete-user"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DeleteUserGroup(ctx, admin.ID, group.ID, customers.DeleteRequest{Revision: group.Revision}, "delete-group"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dsn, auth.Administrator{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Customer(ctx, customer.ID); !errors.Is(err, faults.ErrNotFound) {
		t.Fatalf("deleted user returned: %v", err)
	}
	svc = customers.NewService(s, nil)
	if replay, err := svc.DeleteCustomer(ctx, admin.ID, customer.ID, customers.DeleteRequest{Revision: customer.Revision}, "delete-user"); err != nil || !replay {
		t.Fatalf("user replay: %v", err)
	}
	if replay, err := svc.DeleteUserGroup(ctx, admin.ID, group.ID, customers.DeleteRequest{Revision: group.Revision}, "delete-group"); err != nil || !replay {
		t.Fatalf("group replay: %v", err)
	}
}
