package postgressnapshot

import (
	"context"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

func (s *Store) VLESSSetupAdministrator(ctx context.Context, ruleID string) (string, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) (string, error) {
		return state.VLESSSetupAdministrator(ctx, ruleID)
	})
}
