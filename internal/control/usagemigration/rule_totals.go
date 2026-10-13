package usagemigration

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
)

const ruleTotalsUpSHA = "7624cadd1971f35623e36ada998bf80cb7d276b225a27cb231dbad7c38cbabae"
const ruleTotalsDownSHA = "be89d16388d356e857831ce4b718bfc5c4ee4768201b17de701f79f5f0e27b70"

//go:embed sql/000006_rule_usage_totals.up.sql
var ruleTotalsUp []byte

//go:embed sql/000006_rule_usage_totals.down.sql
var ruleTotalsDown []byte

func ruleTotalsApplied(ctx context.Context, tx *sql.Tx) (bool, error) {
	var name, up, down string
	err := tx.QueryRowContext(ctx, `SELECT name,up_sha256,down_sha256 FROM nyvp_schema_migrations WHERE version=6`).Scan(&name, &up, &down)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if name != "rule_usage_totals" || up != ruleTotalsUpSHA || down != ruleTotalsDownSHA {
		return false, ErrChecksumMismatch
	}
	return true, nil
}

func verifyRuleTotals(ctx context.Context, tx *sql.Tx) error {
	applied, err := ruleTotalsApplied(ctx, tx)
	if err != nil {
		return err
	}
	if !applied {
		return ErrNotApplied
	}
	relation := relationSpec{name: "usage_rule_totals", columns: []columnSpec{{name: "rule_id", dataType: "text"}, {name: "period", dataType: "text"}, {name: "charged_bytes", dataType: "numeric"}}, constraints: []constraintSpec{{name: "usage_rule_totals_pkey", kind: "p"}}}
	if err := verifyColumns(ctx, tx, relation); err != nil {
		return err
	}
	if err := verifyConstraints(ctx, tx, relation); err != nil {
		return err
	}
	for trigger, function := range map[string]string{
		"usage_rule_totals_insert": "usage_rule_totals_added", "usage_rule_totals_delete": "usage_rule_totals_removed",
		"usage_rule_totals_update_1_remove": "usage_rule_totals_removed", "usage_rule_totals_update_2_add": "usage_rule_totals_added",
		"usage_rule_totals_truncate": "usage_rule_totals_cleared",
	} {
		var valid bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_trigger t JOIN pg_proc p ON p.oid=t.tgfoid
        WHERE t.tgrelid='usage_events'::regclass AND t.tgname=$1 AND t.tgenabled='O' AND p.proname=$2 AND NOT t.tgisinternal)`, trigger, function).Scan(&valid)
		if err != nil {
			return err
		}
		if !valid {
			return driftf("rule usage totals trigger %s is missing or disabled", trigger)
		}
	}
	return nil
}

func (runner *Runner) applyRuleTotals(ctx context.Context, tx *sql.Tx) (bool, error) {
	applied, err := ruleTotalsApplied(ctx, tx)
	if err != nil {
		return false, err
	}
	if applied {
		return false, verifyRuleTotals(ctx, tx)
	}
	kind, err := relationKind(ctx, tx, "usage_rule_totals")
	if err != nil {
		return false, err
	}
	if kind != "" {
		return false, ErrUnmanagedObjects
	}
	body, err := prepareMigration(ruleTotalsUp, ruleTotalsUpSHA)
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, body); err != nil {
		return false, fmt.Errorf("apply rule usage totals: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO nyvp_schema_migrations(version,name,up_sha256,down_sha256,applied_at) VALUES(6,'rule_usage_totals',$1,$2,$3)`, ruleTotalsUpSHA, ruleTotalsDownSHA, runner.now().UTC()); err != nil {
		return false, err
	}
	return true, verifyRuleTotals(ctx, tx)
}

func rollbackRuleTotals(ctx context.Context, tx *sql.Tx) error {
	applied, err := ruleTotalsApplied(ctx, tx)
	if err != nil || !applied {
		return err
	}
	body, err := prepareMigration(ruleTotalsDown, ruleTotalsDownSHA)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, body); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM nyvp_schema_migrations WHERE version=6`)
	return err
}
