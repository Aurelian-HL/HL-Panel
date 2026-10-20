package postgressnapshot

import (
	"context"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

func (s *Store) ManagedForwardingRule(ctx context.Context, adminID, id string) (forwarding.Rule, error) {
	return readForwarding(ctx, s, func(view *memoryrepo.ForwardingReadView) (forwarding.Rule, error) {
		return view.ManagedForwardingRule(ctx, adminID, id)
	})
}

func (s *Store) ListManagedForwardingRules(ctx context.Context, adminID string) ([]forwarding.Rule, error) {
	return readForwarding(ctx, s, func(state *memoryrepo.ForwardingReadView) ([]forwarding.Rule, error) {
		return state.ListManagedForwardingRules(ctx, adminID)
	})
}
