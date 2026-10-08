package postgressnapshot

import (
	"context"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"time"
)

func (s *Store) SyncRuleTraffic(ctx context.Context, id string, total int64, at time.Time) error {
	_, err := transact(ctx, s, true, func(m *memoryrepo.Store) (struct{}, error) { return struct{}{}, m.SyncRuleTraffic(ctx, id, total, at) })
	return err
}
