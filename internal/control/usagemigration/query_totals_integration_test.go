package usagemigration_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/usage"
	usagepostgres "github.com/hongle/hl-panel/internal/control/usage/postgres"
	"github.com/hongle/hl-panel/internal/control/usagemigration"
)

func TestQueryTotalsUpgradeBackfillIndexDriftAndRetry(t *testing.T) {
	db := isolatedMigrationDatabase(t)
	ctx := context.Background()
	runner, err := usagemigration.New(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runner.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	// Simulate an already installed v6 ledger with existing user traffic.
	down, err := os.ReadFile("sql/000007_usage_query_totals.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(down)), "BEGIN;")), "COMMIT;"))
	if _, err = db.Exec(body + `; DELETE FROM nyvp_schema_migrations WHERE version=7`); err != nil {
		t.Fatal(err)
	}
	service := usage.NewService(usagepostgres.New(db), noLimitPolicy{}, func() time.Time { return time.Date(2026, 10, 3, 2, 1, 0, 0, time.UTC) })
	if _, err = service.Ingest(ctx, usageReportForMigrationTest()); err != nil {
		t.Fatal(err)
	}
	// A conflicting index must roll back the complete v7 upgrade.
	if _, err = db.Exec(`CREATE TABLE totals_blocker(id text); CREATE INDEX usage_query_totals_rule ON totals_blocker(id)`); err != nil {
		t.Fatal(err)
	}
	if _, err = runner.Apply(ctx); err == nil {
		t.Fatal("conflicting migration succeeded")
	}
	if relationExists(t, db, "usage_query_totals") {
		t.Fatal("failed upgrade leaked table")
	}
	if _, err = db.Exec(`DROP TABLE totals_blocker`); err != nil {
		t.Fatal(err)
	}
	if _, err = runner.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := usagepostgres.New(db).Query(ctx, usage.Query{Scope: usage.ScopeSite, Page: 1, PageSize: 20})
	if err != nil || result.Total != 1 || result.Totals.ChargedBytes != 64 {
		t.Fatalf("backfill: %+v %v", result, err)
	}
	if _, err = db.Exec(`DROP INDEX usage_query_totals_rule`); err != nil {
		t.Fatal(err)
	}
	if err = runner.Verify(ctx); !errors.Is(err, usagemigration.ErrSchemaDrift) {
		t.Fatalf("index drift not detected: %v", err)
	}
	if _, err = db.Exec(`CREATE INDEX usage_query_totals_rule ON usage_query_totals(rule_id,period)`); err != nil {
		t.Fatal(err)
	}
	if err = runner.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Ingest(ctx, usageReportForMigrationTest()); err != nil {
		t.Fatal(err)
	}
	result, err = usagepostgres.New(db).Query(ctx, usage.Query{Scope: usage.ScopeSite, Page: 1, PageSize: 20})
	if err != nil || result.Total != 1 || result.Totals.ChargedBytes != 64 {
		t.Fatal("replay doubled totals", result, err)
	}
}
