package usagemigration_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/usage"
	usagepostgres "github.com/hongle/hl-panel/internal/control/usage/postgres"
	"github.com/hongle/hl-panel/internal/control/usagemigration"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func isolatedMigrationDatabase(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("NYVP_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set NYVP_TEST_POSTGRES_URL for an explicitly isolated PostgreSQL test database")
	}
	if os.Getenv("NYVP_TEST_POSTGRES_ALLOW_MUTATION") != "true" {
		t.Fatal("PostgreSQL test requires explicit NYVP_TEST_POSTGRES_ALLOW_MUTATION=true")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatal("test connection must be a PostgreSQL URL")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal("open isolated PostgreSQL test database failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var databaseName string
	if err := admin.QueryRowContext(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		admin.Close()
		t.Fatal("connect to isolated PostgreSQL test database failed")
	}
	if !strings.HasPrefix(databaseName, "nyvp_") || !strings.HasSuffix(databaseName, "_test") {
		admin.Close()
		t.Fatal("refusing to mutate database: name must match nyvp_*_test")
	}
	schema := fmt.Sprintf("usage_migration_test_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		admin.Close()
		t.Fatal("create isolated migration test schema failed")
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		_, _ = admin.ExecContext(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		admin.Close()
		t.Fatal("open schema-isolated migration database failed")
	}
	t.Cleanup(func() {
		_ = db.Close()
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := admin.ExecContext(cleanupContext, `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Error("clean up migration test schema failed")
		}
		_ = admin.Close()
	})
	return db
}

type noLimitPolicy struct{}

func (noLimitPolicy) UsagePolicy(_ context.Context, customerID string) (usage.CustomerPolicy, error) {
	return usage.CustomerPolicy{CustomerID: customerID}, nil
}

func usageReportForMigrationTest() usage.Report {
	ended := time.Date(2026, 10, 3, 2, 0, 0, 0, time.UTC)
	return usage.Report{
		NodeID: "migration-node", BootID: "migration-boot", Sequence: 1,
		CustomerID: "migration-customer", RuleID: "migration-rule",
		EntryGroupID: "migration-entry", ExitGroupID: "migration-exit", Protocol: "tcp",
		OccurredAt: ended, PeriodStartedAt: ended.Add(-time.Minute), PeriodEndedAt: ended,
		RuleActualBytes: 64, CustomerActualBytes: 64,
		EntryMultiplierMicros: usage.MultiplierScale, ExitMultiplierMicros: usage.MultiplierScale,
	}
}

func TestMigrationApplyVerifyRepositoryCompatibilityAndProtectedRollback(t *testing.T) {
	db := isolatedMigrationDatabase(t)
	runner, err := usagemigration.New(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	status, err := runner.Status(ctx)
	if err != nil || status.State != usagemigration.StatePending {
		t.Fatalf("initial status = %#v, error=%v", status, err)
	}
	applied, err := runner.Apply(ctx)
	if err != nil || !applied.Applied {
		t.Fatalf("apply result = %#v, error=%v", applied, err)
	}
	if err := runner.Verify(ctx); err != nil {
		t.Fatal("verify applied schema:", err)
	}
	replayed, err := runner.Apply(ctx)
	if err != nil || replayed.Applied {
		t.Fatalf("idempotent apply result = %#v, error=%v", replayed, err)
	}

	service := usage.NewService(usagepostgres.New(db), noLimitPolicy{}, func() time.Time {
		return time.Date(2026, 10, 3, 2, 1, 0, 0, time.UTC)
	})
	result, err := service.Ingest(ctx, usageReportForMigrationTest())
	if err != nil {
		t.Fatal("usage repository is incompatible with migrated schema:", err)
	}
	if result.Event.ChargedBytes != 64 || result.Replayed {
		t.Fatalf("unexpected repository ingest: %#v", result)
	}
	if _, err := runner.Rollback(ctx, false); !errors.Is(err, usagemigration.ErrRollbackDataExists) {
		t.Fatalf("non-force rollback error = %v", err)
	}
	if err := runner.Verify(ctx); err != nil {
		t.Fatal("refused rollback changed schema or state:", err)
	}
	rolledBack, err := runner.Rollback(ctx, true)
	if err != nil || !rolledBack.RolledBack {
		t.Fatalf("forced rollback result = %#v, error=%v", rolledBack, err)
	}
	status, err = runner.Status(ctx)
	if err != nil || status.State != usagemigration.StatePending {
		t.Fatalf("post-rollback status = %#v, error=%v", status, err)
	}
}

func TestMigrationFailedApplyRollsBackAllCreatedObjectsAndState(t *testing.T) {
	db := isolatedMigrationDatabase(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE migration_index_blocker (id text);
		CREATE INDEX usage_events_customer_time_idx ON migration_index_blocker(id)`); err != nil {
		t.Fatal(err)
	}
	runner, err := usagemigration.New(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Apply(ctx); err == nil {
		t.Fatal("migration unexpectedly succeeded with a conflicting index")
	}
	for _, name := range []string{
		"nyvp_schema_migrations", "usage_ingest_cursors", "usage_events", "usage_customer_totals",
		"usage_enforcement_decisions", "usage_enforcement_results",
	} {
		if relationExists(t, db, name) {
			t.Fatalf("failed apply leaked relation %s", name)
		}
	}
	if _, err := db.ExecContext(ctx, `DROP INDEX usage_events_customer_time_idx`); err != nil {
		t.Fatal(err)
	}
	if result, err := runner.Apply(ctx); err != nil || !result.Applied {
		t.Fatalf("clean retry after failed transaction = %#v, error=%v", result, err)
	}
}

func TestMigrationFailedDownRollsBackDroppedTablesAndState(t *testing.T) {
	db := isolatedMigrationDatabase(t)
	ctx := context.Background()
	runner, err := usagemigration.New(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE VIEW usage_events_dependency AS SELECT node_id FROM usage_events`); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Rollback(ctx, true); err == nil {
		t.Fatal("rollback unexpectedly ignored a dependent view")
	}
	if err := runner.Verify(ctx); err != nil {
		t.Fatal("failed down transaction changed migrated schema or state:", err)
	}
	if _, err := db.ExecContext(ctx, `DROP VIEW usage_events_dependency`); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Rollback(ctx, false); err != nil {
		t.Fatal("rollback after removing dependency:", err)
	}
}

func relationExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var exists bool
	if err := db.QueryRow(`SELECT EXISTS (
		SELECT 1 FROM pg_catalog.pg_class rel
		JOIN pg_catalog.pg_namespace ns ON ns.oid=rel.relnamespace
		WHERE ns.nspname=current_schema() AND rel.relname=$1
	)`, name).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}
