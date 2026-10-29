package memoryrepo

import (
	"context"
	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/faults"
	"sort"
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

func (s *Store) UsageRuleConfigInput(ctx context.Context, nodeID, ruleID string, beforeGeneration int64) (deploymentreceipts.HistoricalInput, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	versions := make([]int64, 0, len(s.nodeConfigsByNode[nodeID]))
	for generation := range s.nodeConfigsByNode[nodeID] {
		if generation > 0 && generation < beforeGeneration {
			versions = append(versions, generation)
		}
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] > versions[j] })
	for _, generation := range versions {
		if err := ctx.Err(); err != nil {
			return deploymentreceipts.HistoricalInput{}, err
		}
		config := s.nodeConfigsByNode[nodeID][generation]
		contains, err := deploymentreceipts.ContainsUsageRule(deploymentreceipts.HistoricalInput{Config: config}, ruleID)
		if err != nil {
			return deploymentreceipts.HistoricalInput{}, err
		}
		if contains {
			h := NodeConfigHistory{config, s.applyResultsByNode[nodeID][generation], s.applyAttemptsByNode[nodeID][generation]}
			return h.UsageInput(), nil
		}
	}
	return deploymentreceipts.HistoricalInput{}, faults.ErrNotFound
}
