package postgressnapshot

import (
	"context"
	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/nodeupdates"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	"time"
)

func (s *Store) ReplayNodeUpdate(ctx context.Context, admin, node, key, version string) (*nodeupdates.Task, error) {
	return transact(ctx, s, false, func(m *memoryrepo.Store) (*nodeupdates.Task, error) {
		return m.ReplayNodeUpdate(ctx, admin, node, key, version)
	})
}
func (s *Store) LatestNodeUpdate(ctx context.Context, node string) (*nodeupdates.Task, error) {
	return transact(ctx, s, false, func(m *memoryrepo.Store) (*nodeupdates.Task, error) { return m.LatestNodeUpdate(ctx, node) })
}
func (s *Store) CreateNodeUpdate(ctx context.Context, r nodeupdates.Record, event audit.Event) (nodeupdates.Task, error) {
	return transactNoopAware(ctx, s, func(m *memoryrepo.Store) (nodeupdates.Task, error) { return m.CreateNodeUpdate(ctx, r, event) })
}
func (s *Store) ClaimNodeUpdate(ctx context.Context, node, lease string, now time.Time) (*nodeupdates.Record, error) {
	return transactNoopAware(ctx, s, func(m *memoryrepo.Store) (*nodeupdates.Record, error) {
		return m.ClaimNodeUpdate(ctx, node, lease, now)
	})
}
func (s *Store) ReportNodeUpdate(ctx context.Context, node string, in agentv1.NodeUpdateResult, now time.Time) error {
	_, err := transactNoopAware(ctx, s, func(m *memoryrepo.Store) (bool, error) { return true, m.ReportNodeUpdate(ctx, node, in, now) })
	return err
}
func (s *Store) CancelNodeUpdate(ctx context.Context, admin, node, task string, now time.Time) (nodeupdates.Task, error) {
	return transactNoopAware(ctx, s, func(m *memoryrepo.Store) (nodeupdates.Task, error) {
		return m.CancelNodeUpdate(ctx, admin, node, task, now)
	})
}
