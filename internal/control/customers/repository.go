package customers

import (
	"context"

	"github.com/hongle/hl-panel/internal/control/audit"
)

type SaveCustomerInput struct {
	Customer         Customer
	ExpectedRevision int64
	IdempotencyKey   string
	RequestSHA256    string
	CreatedBy        string
	// PasswordChanged distinguishes an explicit reset from a blank-password
	// edit. Repositories preserve the latest hash inside their transaction when
	// false; a hash read earlier by the service must never overwrite a reset.
	PasswordChanged bool
}

type SaveUserGroupInput struct {
	UserGroup        UserGroup
	ExpectedRevision int64
	IdempotencyKey   string
	RequestSHA256    string
	CreatedBy        string
}

// Repositories atomically check replay, optimistic revision, unique names and
// referenced group existence/roles before saving the model and its audit event.
// The replay check precedes revision checks so a retried successful PUT is safe.
type Repository interface {
	ListCustomers(context.Context) ([]Customer, error)
	Customer(context.Context, string) (Customer, error)
	SaveCustomer(context.Context, SaveCustomerInput, audit.Event) (Customer, bool, error)
	ListUserGroups(context.Context) ([]UserGroup, error)
	UserGroup(context.Context, string) (UserGroup, error)
	SaveUserGroup(context.Context, SaveUserGroupInput, audit.Event) (UserGroup, bool, error)
}
