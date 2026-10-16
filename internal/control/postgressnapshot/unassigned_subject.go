package postgressnapshot

import (
	"context"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

func (s *Store) ValidateUnassignedSubject(ctx context.Context, subject string) error {
	_, err := transact(ctx, s, false, func(m *memoryrepo.Store) (struct{}, error) {
		return struct{}{}, m.ValidateUnassignedSubject(ctx, subject)
	})
	return err
}
