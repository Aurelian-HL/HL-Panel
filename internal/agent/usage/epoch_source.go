package usage

import "context"

// EpochSource separates cumulative counters of different owned processes,
// including restarts whose first sample exceeds the previous process's total.
type EpochSource struct {
	Source          CounterSource
	Epoch           func() uint64
	epoch           uint64
	checkpointEpoch uint64
}

func (s *EpochSource) CollectUsage(ctx context.Context, w CollectionWindow) ([]CounterDelta, error) {
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
	s.checkpointEpoch = s.epoch
	if c, ok := s.Source.(CheckpointedCounterSource); ok {
		return c.BeginCollection()
	}
	return nil
}
func (s *EpochSource) CommitCollection() {
	if c, ok := s.Source.(CheckpointedCounterSource); ok {
		c.CommitCollection()
	}
}
func (s *EpochSource) RollbackCollection() {
	s.epoch = s.checkpointEpoch
	if c, ok := s.Source.(CheckpointedCounterSource); ok {
		c.RollbackCollection()
	}
}
