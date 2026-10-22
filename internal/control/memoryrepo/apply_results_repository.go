package memoryrepo

import (
	"context"
	"fmt"
	"maps"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func (s *Store) RecordApplyResults(ctx context.Context, results []generations.ApplyResult, events []audit.Event) (node nodes.Node, err error) {
	if len(results) != 2 || len(events) != 2 || results[0].NodeID != results[1].NodeID {
		return nodes.Node{}, fmt.Errorf("%w: invalid apply receipt batch", faults.ErrValidation)
	}
	requests := make([]agentv1.ApplyResultRequest, 2)
	for i, result := range results {
		requests[i] = agentv1.ApplyResultRequest{Generation: agentv1.NodeConfigGeneration(result.Generation),
			AttemptID: result.AttemptID, Phase: result.Phase, Status: result.Status,
			ConfigSHA256: result.ConfigSHA256, EngineMode: result.EngineMode}
	}
	if !agentv1.IsSuccessfulApplyPair(requests) {
		return nodes.Node{}, fmt.Errorf("%w: expected commit and verify success for one attempt", faults.ErrValidation)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := results[0].NodeID
	previous, exists := s.nodes[id]
	if !exists || previous.DeletedAt != nil {
		return nodes.Node{}, faults.ErrNotFound
	}
	// Each single-result mutation replaces its phase map and attempt SeenIDs.
	// Copy only the affected node's indexes; immutable configurations are shared.
	configs, hadConfigs := s.nodeConfigsByNode[id]
	phaseResults, hadResults := s.applyResultsByNode[id]
	attempts, hadAttempts := s.applyAttemptsByNode[id]
	s.nodeConfigsByNode[id] = maps.Clone(configs)
	s.applyResultsByNode[id] = maps.Clone(phaseResults)
	s.applyAttemptsByNode[id] = maps.Clone(attempts)
	if s.applyResultsByNode[id] == nil {
		s.applyResultsByNode[id] = make(map[int64]map[string]generations.ApplyResult)
	}
	if s.applyAttemptsByNode[id] == nil {
		s.applyAttemptsByNode[id] = make(map[int64]generations.ApplyAttemptState)
	}
	auditLength := len(s.auditEvents)
	defer func() {
		if err == nil {
			return
		}
		s.nodes[id] = previous
		if hadConfigs {
			s.nodeConfigsByNode[id] = configs
		} else {
			delete(s.nodeConfigsByNode, id)
		}
		if hadResults {
			s.applyResultsByNode[id] = phaseResults
		} else {
			delete(s.applyResultsByNode, id)
		}
		if hadAttempts {
			s.applyAttemptsByNode[id] = attempts
		} else {
			delete(s.applyAttemptsByNode, id)
		}
		s.auditEvents = s.auditEvents[:auditLength]
	}()
	for i, result := range results {
		if _, err = s.recordApplyResultLocked(ctx, result, events[i], false); err != nil {
			return nodes.Node{}, err
		}
	}
	if err = ctx.Err(); err != nil {
		return nodes.Node{}, err
	}
	// Compile dependent UDP entry configurations only once after both phases.
	// That compiler restores all affected nodes/configurations on failure.
	if err = s.recompileReliableUDPAfterReceiptLocked(id, results[1].CreatedAt); err != nil {
		return nodes.Node{}, err
	}
	return cloneNode(s.nodes[id]), nil
}
