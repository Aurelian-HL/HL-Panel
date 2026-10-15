package usagemigration_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/hongle/hl-panel/internal/control/usagemigration"
)

func TestExistingLedgerUpgradeBackfillsRuleTotalsAndDetectsTriggerDrift(t *testing.T) {
	db := isolatedMigrationDatabase(t)
	ctx := context.Background()
	runner, err := usagemigration.New(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runner.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	_, file, _, _ := runtime.Caller(0)
	down, err := os.ReadFile(filepath.Join(filepath.Dir(file), "sql/000006_rule_usage_totals.down.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `DELETE FROM nyvp_schema_migrations WHERE version=6;
 INSERT INTO usage_ingest_cursors VALUES('node','boot',100,now());
 INSERT INTO usage_events SELECT 'node','boot',i+1,repeat('a',64),'customer','rule','entry','','tcp',
 '2026-01-31 23:59:59+00'::timestamptz + i*interval '1 second',now()-interval '1 second',now(),10,10,10,1000000,1000000,now() FROM generate_series(0,99) i`); err != nil {
		t.Fatal(err)
	}
	status, err := runner.Status(ctx)
	if err != nil || status.State != usagemigration.StatePending {
		t.Fatal(status, err)
	}
	result, err := runner.Apply(ctx)
	if err != nil || !result.Applied {
		t.Fatal(result, err)
	}
	var total, jan, feb int64
	if err = db.QueryRowContext(ctx, `SELECT sum(charged_bytes) FILTER(WHERE period=''),sum(charged_bytes) FILTER(WHERE period='2026-01'),sum(charged_bytes) FILTER(WHERE period='2026-02') FROM usage_rule_totals`).Scan(&total, &jan, &feb); err != nil {
		t.Fatal(err)
	}
	if total != 1000 || jan != 10 || feb != 990 {
		t.Fatal("incorrect backfill", total, jan, feb)
	}
	if result, err = runner.Apply(ctx); err != nil || result.Applied {
		t.Fatal("upgrade replay", result, err)
	}
	if _, err = db.ExecContext(ctx, `ALTER TABLE usage_events DISABLE TRIGGER usage_rule_totals_insert`); err != nil {
		t.Fatal(err)
	}
	if err = runner.Verify(ctx); err == nil {
		t.Fatal("disabled accounting trigger accepted")
	}
}
