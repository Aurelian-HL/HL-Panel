package postgressnapshot

import (
	"context"
	"database/sql"
	"errors"

	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

// Always validate the database row, including xmin for same-revision repairs.
// The detached projection contains no connection or mutable transaction state.
func readUsage[T any](parent context.Context, s *Store, operation func(context.Context, *sql.Tx, *memoryrepo.UsageReadView) (T, error)) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(parent, operationTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return zero, errors.New("PostgreSQL transaction unavailable")
	}
	defer tx.Rollback()
	cached, _, err := s.validatedReadSnapshot(ctx, tx)
	if err != nil {
		return zero, err
	}
	result, err := operation(ctx, tx, cached.usage)
	if err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return zero, errors.New("PostgreSQL transaction commit failed")
	}
	return result, nil
}

func (s *Store) RuleTrafficState(ctx context.Context, id string) (forwarding.RuleTrafficState, error) {
	return readUsage(ctx, s, func(ctx context.Context, _ *sql.Tx, view *memoryrepo.UsageReadView) (forwarding.RuleTrafficState, error) {
		return view.RuleTrafficState(ctx, id)
	})
}

func (s *Store) ListRuleTrafficStates(ctx context.Context) ([]forwarding.RuleTrafficState, error) {
	return readUsage(ctx, s, func(ctx context.Context, _ *sql.Tx, view *memoryrepo.UsageReadView) ([]forwarding.RuleTrafficState, error) {
		return view.ListRuleTrafficStates(ctx)
	})
}
