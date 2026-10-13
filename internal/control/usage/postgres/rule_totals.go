package postgres

import (
	"context"
	"errors"

	"github.com/hongle/hl-panel/internal/control/usage"
)

// RuleChargedBytes uses one MVCC statement and the existing rule/time index.
// It intentionally does not read detail rows or enforcement decisions. Historical
// SUM remains proportional to ledger size; no in-memory totals replace the ledger.
func (repository *Repository) RuleChargedBytes(ctx context.Context, query usage.Query) (int64, error) {
	if repository == nil || repository.db == nil {
		return 0, errors.New("usage PostgreSQL repository is unavailable")
	}
	if query.Scope != usage.ScopeRule || query.ScopeID == "" {
		return 0, errors.New("rule usage scope is required")
	}
	where, args := queryFilter(query)
	var total int64
	if err := repository.db.QueryRowContext(ctx, `SELECT COALESCE(sum(charged_bytes),0)::bigint FROM usage_events e`+where, args...).Scan(&total); err != nil {
		return 0, errors.New("rule usage aggregate query failed")
	}
	return total, nil
}
