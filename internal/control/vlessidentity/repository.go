package vlessidentity

import (
	"context"

	"github.com/hongle/hl-panel/internal/control/audit"
)

// Repository implementations atomically validate all customer/rule/pool
// references, persist the mutation and append its audit event.
type Repository interface {
	Provision(context.Context, ProvisionInput, audit.Event) (Binding, bool, error)
	List(context.Context) ([]Binding, error)
	ListForAdministrator(context.Context, string) ([]Binding, error)
	Rotate(context.Context, RotateInput, audit.Event) (Binding, bool, error)
	Revoke(context.Context, RevokeInput, audit.Event) (Binding, bool, error)
}

// CredentialForAdministrator is intentionally kept separate from the public
// binding list: it is only used by the authenticated admin connection export
// path and returns the confidential UUID in-process.
type CredentialReader interface {
	CredentialForAdministrator(context.Context, string, string) (CredentialRecord, error)
	CredentialByIDForAdministrator(context.Context, string, string) (CredentialRecord, error)
}
