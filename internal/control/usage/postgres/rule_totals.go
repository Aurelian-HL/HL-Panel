package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/hongle/hl-panel/internal/control/usage"
)

// RuleChargedBytes reads transactionally maintained lifetime/UTC month totals.
// Arbitrary reporting intervals retain their exact ledger query semantics.
func (repository *Repository) RuleChargedBytes(ctx context.Context, query usage.Query) (int64, error) {
	if repository == nil || repository.db == nil {
		return 0, errors.New("usage PostgreSQL repository is unavailable")
	}
	if query.Scope != usage.ScopeRule || query.ScopeID == "" {
		return 0, errors.New("rule usage scope is required")
	}
	period, summarized := ruleTotalPeriod(query)
	if summarized {
		var total int64
		err := repository.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT charged_bytes FROM usage_rule_totals WHERE rule_id=$1 AND period=$2),0)::bigint`, query.ScopeID, period).Scan(&total)
		if err != nil {
			return 0, errors.New("rule usage totals query failed")
		}
		return total, nil
	}
	where, args := queryFilter(query)
	var total int64
	if err := repository.db.QueryRowContext(ctx, `SELECT COALESCE(sum(charged_bytes),0)::bigint FROM usage_events e`+where, args...).Scan(&total); err != nil {
		return 0, errors.New("rule usage aggregate query failed")
	}
	return total, nil
}

func ruleTotalPeriod(query usage.Query) (string, bool) {
	if query.From == nil && query.To == nil {
		return "", true
	}
	if query.From == nil || query.To == nil {
		return "", false
	}
	from, to := query.From.UTC(), query.To.UTC()
	start := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.UTC)
	if !from.Equal(start) || !to.Equal(start.AddDate(0, 1, 0)) {
		return "", false
	}
	return start.Format("2006-01"), true
}
