package postgressnapshot

import (
	"context"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

var _ customers.DeletionRepository = (*Store)(nil)

func (s *Store) DeleteCustomer(ctx context.Context, input customers.DeleteInput, event audit.Event) (bool, error) {
	return transact(ctx, s, true, func(state *memoryrepo.Store) (bool, error) { return state.DeleteCustomer(ctx, input, event) })
}

func (s *Store) DeleteUserGroup(ctx context.Context, input customers.DeleteInput, event audit.Event) (bool, error) {
	return transact(ctx, s, true, func(state *memoryrepo.Store) (bool, error) { return state.DeleteUserGroup(ctx, input, event) })
}
