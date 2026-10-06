package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/hongle/hl-panel/internal/control/usage"
)

func (repository *Repository) ReplayLegacy(ctx context.Context, report usage.Report) (usage.IngestResult, bool, error) {
	if repository == nil || repository.db == nil {
		return usage.IngestResult{}, false, errors.New("usage PostgreSQL repository is unavailable")
	}
	tx, err := repository.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return usage.IngestResult{}, false, errors.New("usage replay transaction unavailable")
	}
	defer tx.Rollback()

	stored, exists, err := eventByKey(ctx, tx, report.NodeID, report.BootID, report.Sequence)
	if err != nil {
		return usage.IngestResult{}, false, err
	}
	if !exists {
		return usage.IngestResult{}, false, nil
	}
	if !usage.RuleReplayMatches(stored.Event, report) {
		return usage.IngestResult{}, false, usage.ErrIdempotencyConflict
	}
	totals, err := customerTotals(ctx, tx, stored.Event.CustomerID)
	if err != nil {
		return usage.IngestResult{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return usage.IngestResult{}, false, errors.New("usage replay transaction commit failed")
	}
	return usage.IngestResult{
		Event: stored.Event, CustomerTotals: totals, Decision: stored.Decision, Replayed: true,
	}, true, nil
}

var _ usage.LegacyReplayRepository = (*Repository)(nil)
