package postgressnapshot

import (
	"context"
	"database/sql"
	"errors"

	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

func initializeRuleTrafficOverlays(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS hl_panel_rule_traffic (
        rule_id text PRIMARY KEY, rule_revision bigint NOT NULL CHECK(rule_revision>0),
        monthly boolean NOT NULL, period text NOT NULL, used_bytes bigint NOT NULL CHECK(used_bytes>=0), updated_at timestamptz NOT NULL
    )`)
	if err != nil {
		return errors.New("rule traffic persistence initialization failed")
	}
	return nil
}

func readRuleTrafficOverlays(ctx context.Context, tx *sql.Tx, id string) ([]memoryrepo.RuleTrafficOverlay, error) {
	query := `SELECT rule_id,rule_revision,monthly,period,used_bytes,updated_at FROM hl_panel_rule_traffic`
	var args []any
	if id != "" {
		query += ` WHERE rule_id=$1`
		args = append(args, id)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, errors.New("rule traffic projection read failed")
	}
	defer rows.Close()
	var result []memoryrepo.RuleTrafficOverlay
	for rows.Next() {
		var item memoryrepo.RuleTrafficOverlay
		s := &item.State
		if err := rows.Scan(&s.ID, &s.Revision, &s.TrafficQuotaMonthly, &s.TrafficUsagePeriod, &s.TrafficUsedBytes, &item.UpdatedAt); err != nil {
			return nil, errors.New("rule traffic projection decode failed")
		}
		result = append(result, item)
	}
	if rows.Err() != nil {
		return nil, errors.New("rule traffic projection iteration failed")
	}
	return result, nil
}

func mergeRuleTrafficOverlays(ctx context.Context, tx *sql.Tx, state *memoryrepo.Store) error {
	overlays, err := readRuleTrafficOverlays(ctx, tx, "")
	if err != nil {
		return err
	}
	state.MergeRuleTrafficOverlays(overlays)
	return nil
}
