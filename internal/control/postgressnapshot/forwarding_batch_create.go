package postgressnapshot

import (
	"context"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

func (s *Store) CreateForwardingRules(ctx context.Context, input forwarding.BatchCreateInput, event audit.Event) (forwarding.BatchCreateResult, bool, error) {
	type result struct {
		item     forwarding.BatchCreateResult
		replayed bool
	}
	value, err := transactPrepared(ctx, s, func(state *memoryrepo.Store) (result, error) {
		item, replayed, err := state.CreateForwardingRules(ctx, input, event)
		return result{item, replayed}, err
	})
	return value.item, value.replayed, err
}
