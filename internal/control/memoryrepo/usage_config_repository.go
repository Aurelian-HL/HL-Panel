package memoryrepo

import (
	"context"
	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func (s *Store) UsageConfigInput(_ context.Context, nodeID string, generation int64) (deploymentreceipts.HistoricalInput, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	config, ok := s.nodeConfigsByNode[nodeID][generation]
	if !ok {
		return deploymentreceipts.HistoricalInput{}, faults.ErrNotFound
	}
	attempt := s.applyAttemptsByNode[nodeID][generation]
	input := deploymentreceipts.HistoricalInput{Config: cloneNodeConfig(config), AttemptID: attempt.CurrentID,
		Invalidated: attempt.Invalidated, ApplyResults: make(map[agentv1.ApplyPhase]generations.ApplyResult)}
	for phase, result := range s.applyResultsByNode[nodeID][generation] {
		input.ApplyResults[agentv1.ApplyPhase(phase)] = result
	}
	return input, nil
}
