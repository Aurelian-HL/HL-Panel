package postgressnapshot

import (
	"context"
	"database/sql"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

func (s *Store) ValidateUnassignedSubject(ctx context.Context, subject string) error {
	_, err := readUsage(ctx, s, func(ctx context.Context, _ *sql.Tx, view *memoryrepo.UsageReadView) (struct{}, error) {
		return struct{}{}, view.ValidateUnassignedSubject(ctx, subject)
	})
	return err
}
