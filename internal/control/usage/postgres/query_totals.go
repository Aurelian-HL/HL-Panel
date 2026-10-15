package postgres

import (
	"fmt"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/control/usage"
)

// Summaries are exact and maintained in the ledger transaction. Arbitrary
// intervals still use the raw ledger; complete UTC months use smaller tuples.
func queryTotalsSQL(query usage.Query) (string, []any) {
	from, to, summarized := queryTotalPeriods(query)
	if !summarized {
		where, args := queryFilter(query)
		return `SELECT count(*), COALESCE(sum(rule_actual_bytes),0)::bigint,
            COALESCE(sum(customer_actual_bytes),0)::bigint, COALESCE(sum(charged_bytes),0)::bigint FROM usage_events e` + where, args
	}
	conditions := []string{}
	args := []any{}
	parameter := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if from == "" {
		conditions = append(conditions, "period=''")
	} else {
		conditions = append(conditions, "period >= "+parameter(from), "period < "+parameter(to))
	}
	switch query.Scope {
	case usage.ScopeCustomer:
		conditions = append(conditions, "customer_id="+parameter(query.ScopeID))
	case usage.ScopeRule:
		conditions = append(conditions, "rule_id="+parameter(query.ScopeID))
	case usage.ScopeDeviceGroup:
		p := parameter(query.ScopeID)
		// Each tuple is selected once even if entry and exit share the group.
		conditions = append(conditions, "(entry_group_id="+p+" OR exit_group_id="+p+")")
	}
	return `SELECT COALESCE(sum(event_count),0)::bigint, COALESCE(sum(rule_actual_bytes),0)::bigint,
        COALESCE(sum(customer_actual_bytes),0)::bigint, COALESCE(sum(charged_bytes),0)::bigint
        FROM usage_query_totals WHERE ` + strings.Join(conditions, " AND "), args
}

func queryTotalPeriods(query usage.Query) (string, string, bool) {
	if query.From == nil && query.To == nil {
		return "", "", true
	}
	if query.From == nil || query.To == nil {
		return "", "", false
	}
	from, to := query.From.UTC(), query.To.UTC()
	start := func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC) }
	if !from.Equal(start(from)) || !to.Equal(start(to)) || !to.After(from) {
		return "", "", false
	}
	// PostgreSQL's stored YYYY-MM keys are four digit AD years.
	if from.Year() < 1 || to.Year() > 9999 {
		return "", "", false
	}
	return from.Format("2006-01"), to.Format("2006-01"), true
}
