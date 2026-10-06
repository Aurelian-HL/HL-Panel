package usage

import "context"

// ConditionalSource skips an engine that is absent from the applied bundle.
// It never uses process health to suppress a configured engine's failure.
type ConditionalSource struct {
	Source   CounterSource
	Expected func() bool
}

func (s ConditionalSource) CollectUsage(ctx context.Context, window CollectionWindow) ([]CounterDelta, error) {
	if s.Source == nil || s.Expected == nil {
		return nil, ErrCounterSourceUnavailable
	}
	if !s.Expected() {
		return nil, nil
	}
	return s.Source.CollectUsage(ctx, window)
}
func (s ConditionalSource) BeginCollection() error {
	if c, ok := s.Source.(CheckpointedCounterSource); ok {
		return c.BeginCollection()
	}
	return nil
}
func (s ConditionalSource) CommitCollection() {
	if c, ok := s.Source.(CheckpointedCounterSource); ok {
		c.CommitCollection()
	}
}
func (s ConditionalSource) RollbackCollection() {
	if c, ok := s.Source.(CheckpointedCounterSource); ok {
		c.RollbackCollection()
	}
}
