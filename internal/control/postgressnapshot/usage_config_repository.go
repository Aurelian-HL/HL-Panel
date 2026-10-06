package postgressnapshot

import (
	"context"
	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

func (s *Store) UsageConfigInput(ctx context.Context, nodeID string, generation int64) (deploymentreceipts.HistoricalInput, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) (deploymentreceipts.HistoricalInput, error) {
		return state.UsageConfigInput(ctx, nodeID, generation)
	})
}
