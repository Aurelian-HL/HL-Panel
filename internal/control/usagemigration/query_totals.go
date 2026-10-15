package usagemigration

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
)

const queryTotalsUpSHA = "f47193088393d934a4f8a25f8b0170e6a50bab45e92177ba988fa88213c6bf39"
const queryTotalsDownSHA = "4a4c0b4d081c86fe1ea9cbbce9c7ded7edd395b0475bc103c12ddc7e049fcb7a"

//go:embed sql/000007_usage_query_totals.up.sql
var queryTotalsUp []byte

//go:embed sql/000007_usage_query_totals.down.sql
var queryTotalsDown []byte

func queryTotalsApplied(ctx context.Context, tx *sql.Tx) (bool, error) {
	var name, up, down string
	err := tx.QueryRowContext(ctx, `SELECT name,up_sha256,down_sha256 FROM nyvp_schema_migrations WHERE version=7`).Scan(&name, &up, &down)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if name != "usage_query_totals" || up != queryTotalsUpSHA || down != queryTotalsDownSHA {
		return false, ErrChecksumMismatch
	}
	return true, nil
}

func verifyQueryTotals(ctx context.Context, tx *sql.Tx) error {
	applied, err := queryTotalsApplied(ctx, tx)
	if err != nil {
		return err
	}
	if !applied {
		return ErrNotApplied
	}
	relation := relationSpec{name: "usage_query_totals", columns: []columnSpec{
		{name: "node_id", dataType: "text"}, {name: "customer_id", dataType: "text"}, {name: "rule_id", dataType: "text"},
		{name: "entry_group_id", dataType: "text"}, {name: "exit_group_id", dataType: "text"}, {name: "period", dataType: "text"},
		{name: "event_count", dataType: "numeric"}, {name: "rule_actual_bytes", dataType: "numeric"},
		{name: "customer_actual_bytes", dataType: "numeric"}, {name: "charged_bytes", dataType: "numeric"},
	}, constraints: []constraintSpec{{name: "usage_query_totals_pkey", kind: "p"}}}
	if err := verifyColumns(ctx, tx, relation); err != nil {
		return err
	}
	if err := verifyConstraints(ctx, tx, relation); err != nil {
		return err
	}
	for _, name := range []string{"usage_query_totals_pkey", "usage_query_totals_customer", "usage_query_totals_rule", "usage_query_totals_entry", "usage_query_totals_exit", "usage_query_totals_period"} {
		var valid bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid WHERE i.indrelid='usage_query_totals'::regclass AND c.relname=$1 AND i.indisvalid AND i.indisready)`, name).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return driftf("usage query totals index %s is missing or invalid", name)
		}
	}
	for trigger, function := range map[string]string{
		"usage_query_totals_insert": "usage_query_totals_added", "usage_query_totals_delete": "usage_query_totals_removed",
		"usage_query_totals_update_1_remove": "usage_query_totals_removed", "usage_query_totals_update_2_add": "usage_query_totals_added",
		"usage_query_totals_truncate": "usage_query_totals_cleared",
	} {
		var valid bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_trigger t JOIN pg_proc p ON p.oid=t.tgfoid
        WHERE t.tgrelid='usage_events'::regclass AND t.tgname=$1 AND t.tgenabled='O' AND p.proname=$2 AND NOT t.tgisinternal)`, trigger, function).Scan(&valid)
		if err != nil {
			return err
		}
		if !valid {
			return driftf("usage query totals trigger %s is missing or disabled", trigger)
		}
	}
	return nil
}

func (runner *Runner) applyQueryTotals(ctx context.Context, tx *sql.Tx) (bool, error) {
	applied, err := queryTotalsApplied(ctx, tx)
	if err != nil {
		return false, err
	}
	if applied {
		return false, verifyQueryTotals(ctx, tx)
	}
	kind, err := relationKind(ctx, tx, "usage_query_totals")
	if err != nil {
		return false, err
	}
	if kind != "" {
		return false, ErrUnmanagedObjects
	}
	body, err := prepareMigration(queryTotalsUp, queryTotalsUpSHA)
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, body); err != nil {
		return false, fmt.Errorf("apply usage query totals: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO nyvp_schema_migrations(version,name,up_sha256,down_sha256,applied_at) VALUES(7,'usage_query_totals',$1,$2,$3)`, queryTotalsUpSHA, queryTotalsDownSHA, runner.now().UTC()); err != nil {
		return false, err
	}
	return true, verifyQueryTotals(ctx, tx)
}

func rollbackQueryTotals(ctx context.Context, tx *sql.Tx) error {
	applied, err := queryTotalsApplied(ctx, tx)
	if err != nil || !applied {
		return err
	}
	body, err := prepareMigration(queryTotalsDown, queryTotalsDownSHA)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, body); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM nyvp_schema_migrations WHERE version=7`)
	return err
}
