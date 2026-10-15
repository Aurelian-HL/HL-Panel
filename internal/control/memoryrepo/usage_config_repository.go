package memoryrepo

import (
	"context"
	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
)

func (s *Store) UsageConfigInput(ctx context.Context, nodeID string, generation int64) (deploymentreceipts.HistoricalInput, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	history, err := s.nodeConfigHistoryLocked(ctx, nodeID, generation)
	if err != nil {
		return deploymentreceipts.HistoricalInput{}, err
	}
	return history.UsageInput(), nil
}
