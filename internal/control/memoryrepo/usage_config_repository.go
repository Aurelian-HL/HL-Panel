package memoryrepo

import (
	"context"
	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func (s *Store) UsageConfigInput(ctx context.Context, nodeID string, generation int64) (deploymentreceipts.HistoricalInput, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	history, err := s.nodeConfigHistoryLocked(ctx, nodeID, generation)
	if err != nil {
		return deploymentreceipts.HistoricalInput{}, err
	}
	attempt := history.Attempt
	input := deploymentreceipts.HistoricalInput{Config: history.Config, AttemptID: attempt.CurrentID,
		Invalidated: attempt.Invalidated, ApplyResults: make(map[agentv1.ApplyPhase]generations.ApplyResult)}
	for phase, result := range history.Results {
		input.ApplyResults[agentv1.ApplyPhase(phase)] = result
	}
	return input, nil
}
