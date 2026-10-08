package postgressnapshot

import (
	"context"
	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/subscriptions"
)

var _ subscriptions.Repository = (*Store)(nil)

func (s *Store) ListSubscriptions(ctx context.Context, admin string) ([]subscriptions.Item, error) {
	return transact(ctx, s, false, func(m *memoryrepo.Store) ([]subscriptions.Item, error) { return m.ListSubscriptions(ctx, admin) })
}
func (s *Store) Subscription(ctx context.Context, admin, id string) (subscriptions.Record, error) {
	return transact(ctx, s, false, func(m *memoryrepo.Store) (subscriptions.Record, error) { return m.Subscription(ctx, admin, id) })
}
func (s *Store) SubscriptionByToken(ctx context.Context, hash string) (subscriptions.Record, error) {
	return transact(ctx, s, false, func(m *memoryrepo.Store) (subscriptions.Record, error) { return m.SubscriptionByToken(ctx, hash) })
}
func (s *Store) MutateSubscription(ctx context.Context, c subscriptions.Command, e audit.Event) (subscriptions.Item, bool, error) {
	type result struct {
		item     subscriptions.Item
		replayed bool
	}
	r, err := transact(ctx, s, true, func(m *memoryrepo.Store) (result, error) {
		item, replayed, err := m.MutateSubscription(ctx, c, e)
		return result{item, replayed}, err
	})
	return r.item, r.replayed, err
}
