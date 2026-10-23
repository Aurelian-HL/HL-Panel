package postgressnapshot

import (
	"context"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

var _ customers.ServiceOwnerDeletionRepository = (*Store)(nil)

func (s *Store) DeleteServiceOwner(ctx context.Context, input customers.DeleteInput, event audit.Event) (bool, error) {
	return transact(ctx, s, true, func(state *memoryrepo.Store) (bool, error) {
		return state.DeleteServiceOwner(ctx, input, event)
	})
}
