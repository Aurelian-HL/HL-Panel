package memoryrepo

import (
	"errors"
	"fmt"
	"math"
)

// NodeConfigGenerationFloorReader is attached by the persistence transaction.
// It reads only the node's largest archived generation, without loading its
// confidential configuration or receipts. The closure retains the transaction
// context and must never outlive that transaction.
type NodeConfigGenerationFloorReader func(nodeID string) (int64, error)

func (s *Store) SetNodeConfigGenerationFloorReader(reader NodeConfigGenerationFloorReader) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodeConfigGenerationFloorReader = reader
}

// A recovery can restore the node counters while retaining newer configuration
// history. A generation identifies immutable configuration: never reuse that
// number merely because the desired counter was restored to an older value.
// Call only after the configuration hash changes, keeping no-op compilation
// free of archive queries. The caller already holds the store lock.
func (s *Store) nextNodeConfigGenerationLocked(nodeID string) (int64, error) {
	node := s.nodes[nodeID]
	floor := max(node.DesiredGeneration, node.AppliedGeneration)
	for generation := range s.nodeConfigsByNode[nodeID] {
		floor = max(floor, generation)
	}
	if s.nodeConfigGenerationFloorReader != nil {
		archived, err := s.nodeConfigGenerationFloorReader(nodeID)
		if err != nil {
			return 0, fmt.Errorf("read node configuration generation floor: %w", err)
		}
		if archived < 0 {
			return 0, errors.New("invalid node configuration generation floor")
		}
		floor = max(floor, archived)
	}
	if floor == math.MaxInt64 {
		return 0, errors.New("node configuration generation exhausted")
	}
	return floor + 1, nil
}
