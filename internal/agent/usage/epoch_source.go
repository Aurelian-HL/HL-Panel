package usage

import (
	"context"
	"sync"
)

// EpochSource separates cumulative counters of different owned processes,
// including restarts whose first sample exceeds the previous process's total.
type EpochSource struct {
	Source          CounterSource
	Epoch           func() uint64
	mu              sync.Mutex
	epoch           uint64
	checkpointEpoch uint64
}

func (s *EpochSource) CollectUsage(ctx context.Context, w CollectionWindow) ([]CounterDelta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	epoch := s.Epoch()
	if epoch != s.epoch {
		if reset, ok := s.Source.(interface{ ResetCounters() }); ok {
			reset.ResetCounters()
		}
		s.epoch = epoch
	}
	return s.Source.CollectUsage(ctx, w)
}
func (s *EpochSource) BeginCollection() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkpointEpoch = s.epoch
	if c, ok := s.Source.(CheckpointedCounterSource); ok {
		return c.BeginCollection()
	}
	return nil
}
func (s *EpochSource) CommitCollection() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.Source.(CheckpointedCounterSource); ok {
		c.CommitCollection()
	}
}
func (s *EpochSource) RollbackCollection() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.epoch = s.checkpointEpoch
	if c, ok := s.Source.(CheckpointedCounterSource); ok {
		c.RollbackCollection()
	}
}
