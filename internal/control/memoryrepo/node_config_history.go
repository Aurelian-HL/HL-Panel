package memoryrepo

import (
	"context"
	"errors"
	"maps"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/generations"
)

// NodeConfigHistory is confidential persistence material, never a public DTO.
// A generation and its receipts move together so late usage can still be verified.
type NodeConfigHistory struct {
	Config  generations.NodeConfigGeneration
	Results map[string]generations.ApplyResult
	Attempt generations.ApplyAttemptState
}

type NodeConfigHistoryReader func(context.Context, string, int64) (NodeConfigHistory, error)

func (s *Store) SetNodeConfigHistoryReader(reader NodeConfigHistoryReader) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodeConfigHistoryReader = reader
}

func (s *Store) nodeConfigHistoryLocked(ctx context.Context, nodeID string, generation int64) (NodeConfigHistory, error) {
	if config, ok := s.nodeConfigsByNode[nodeID][generation]; ok {
		return cloneNodeConfigHistory(NodeConfigHistory{config, s.applyResultsByNode[nodeID][generation], s.applyAttemptsByNode[nodeID][generation]}), nil
	}
	if s.nodeConfigHistoryReader == nil {
		return NodeConfigHistory{}, faults.ErrNotFound
	}
	return s.nodeConfigHistoryReader(ctx, nodeID, generation)
}

func cloneNodeConfigHistory(h NodeConfigHistory) NodeConfigHistory {
	h.Config = cloneNodeConfig(h.Config)
	h.Results = maps.Clone(h.Results)
	h.Attempt.SeenIDs = maps.Clone(h.Attempt.SeenIDs)
	return h
}

// DetachHistoricalNodeConfigs keeps desired and applied generations hot.
// The adapter must persist the returned material in the SAME transaction as
// the compact snapshot. Nothing is expired or discarded by this operation.
func (s *Store) DetachHistoricalNodeConfigs() []NodeConfigHistory {
	s.mu.Lock()
	defer s.mu.Unlock()
	var history []NodeConfigHistory
	for nodeID, configs := range s.nodeConfigsByNode {
		node := s.nodes[nodeID]
		for generation, config := range configs {
			if generation == node.DesiredGeneration || generation == node.AppliedGeneration {
				continue
			}
			history = append(history, cloneNodeConfigHistory(NodeConfigHistory{config, s.applyResultsByNode[nodeID][generation], s.applyAttemptsByNode[nodeID][generation]}))
			delete(configs, generation)
			delete(s.applyResultsByNode[nodeID], generation)
			delete(s.applyAttemptsByNode[nodeID], generation)
		}
	}
	return history
}

func ValidateNodeConfigHistory(h NodeConfigHistory, nodeID string, generation int64) error {
	c := h.Config
	if c.NodeID != nodeID || c.Generation != generation || generation <= 0 || c.ID == "" || c.CreatedAt.IsZero() ||
		generations.SHA256Hex(c.Config) != c.ConfigSHA256 {
		return errors.New("invalid persisted node configuration history")
	}
	if h.Attempt.CurrentID != "" {
		if !validPersistedAttemptID(h.Attempt.CurrentID) || !h.Attempt.SeenIDs[h.Attempt.CurrentID] {
			return errors.New("invalid persisted node configuration attempt")
		}
	}
	for id, seen := range h.Attempt.SeenIDs {
		if !seen || !validPersistedAttemptID(id) {
			return errors.New("invalid persisted node configuration attempt history")
		}
	}
	for phase, result := range h.Results {
		if string(result.Phase) != phase || result.NodeID != nodeID || result.Generation != generation || result.ConfigSHA256 != c.ConfigSHA256 || result.AttemptID != h.Attempt.CurrentID {
			return errors.New("invalid persisted node configuration receipt")
		}
	}
	return nil
}

// RestoreNodeConfigHistory joins the archive for a full migration export or
// loads one generation before recording a late deployment receipt.
func (s *Store) RestoreNodeConfigHistory(history []NodeConfigHistory) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, h := range history {
		if err := ValidateNodeConfigHistory(h, h.Config.NodeID, h.Config.Generation); err != nil {
			return err
		}
		if _, ok := s.nodes[h.Config.NodeID]; !ok {
			return errors.New("node configuration history references missing node")
		}
		if current, ok := s.nodeConfigsByNode[h.Config.NodeID][h.Config.Generation]; ok {
			if current.ConfigSHA256 != h.Config.ConfigSHA256 {
				return errors.New("node configuration archive conflicts with current generation")
			}
			// A late receipt can promote an archived generation to applied. Its
			// hot receipts are authoritative until it is archived again.
			continue
		}
		s.restoreNodeConfigHistoryLocked(h)
	}
	return nil
}

func (s *Store) restoreNodeConfigHistoryLocked(h NodeConfigHistory) {
	h = cloneNodeConfigHistory(h)
	id, generation := h.Config.NodeID, h.Config.Generation
	if s.nodeConfigsByNode[id] == nil {
		s.nodeConfigsByNode[id] = make(map[int64]generations.NodeConfigGeneration)
	}
	if s.applyResultsByNode[id] == nil {
		s.applyResultsByNode[id] = make(map[int64]map[string]generations.ApplyResult)
	}
	if s.applyAttemptsByNode[id] == nil {
		s.applyAttemptsByNode[id] = make(map[int64]generations.ApplyAttemptState)
	}
	s.nodeConfigsByNode[id][generation] = h.Config
	s.applyResultsByNode[id][generation] = h.Results
	if h.Attempt.CurrentID != "" {
		s.applyAttemptsByNode[id][generation] = h.Attempt
	}
}
