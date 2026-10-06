package usage

import (
	"context"
	"errors"
	"sync"
)

// MultiSource combines independent engine counters for mixed nodes. Each
// source keeps its own cursor and checkpoint so one engine's reset cannot
// corrupt the other engine's accounting.
type MultiSource struct {
	sources    []CounterSource
	mu         sync.Mutex
	checkpoint bool
}

func NewMultiSource(sources ...CounterSource) (*MultiSource, error) {
	filtered := make([]CounterSource, 0, len(sources))
	for _, source := range sources {
		if source != nil {
			filtered = append(filtered, source)
		}
	}
	if len(filtered) == 0 {
		return nil, ErrCounterSourceUnavailable
	}
	return &MultiSource{sources: filtered}, nil
}

func (source *MultiSource) CollectUsage(ctx context.Context, window CollectionWindow) ([]CounterDelta, error) {
	if source == nil || len(source.sources) == 0 {
		return nil, ErrCounterSourceUnavailable
	}
	var result []CounterDelta
	for _, child := range source.sources {
		deltas, err := child.CollectUsage(ctx, window)
		if err != nil {
			return nil, err
		}
		result = append(result, deltas...)
	}
	return result, nil
}

func (source *MultiSource) BeginCollection() error {
	if source == nil || len(source.sources) == 0 {
		return ErrCounterSourceUnavailable
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.checkpoint {
		return errors.New("multi-source usage collection checkpoint is already active")
	}
	started := make([]CheckpointedCounterSource, 0, len(source.sources))
	for _, child := range source.sources {
		checkpointed, ok := child.(CheckpointedCounterSource)
		if !ok {
			continue
		}
		if err := checkpointed.BeginCollection(); err != nil {
			for _, begun := range started {
				begun.RollbackCollection()
			}
			return err
		}
		started = append(started, checkpointed)
	}
	source.checkpoint = true
	return nil
}

func (source *MultiSource) CommitCollection() {
	if source == nil {
		return
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if !source.checkpoint {
		return
	}
	for _, child := range source.sources {
		if checkpointed, ok := child.(CheckpointedCounterSource); ok {
			checkpointed.CommitCollection()
		}
	}
	source.checkpoint = false
}

func (source *MultiSource) RollbackCollection() {
	if source == nil {
		return
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if !source.checkpoint {
		return
	}
	for _, child := range source.sources {
		if checkpointed, ok := child.(CheckpointedCounterSource); ok {
			checkpointed.RollbackCollection()
		}
	}
	source.checkpoint = false
}
