package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/usage"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func isolatedUsageDatabase(t *testing.T) *sql.DB {
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
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var databaseName string
	if err := admin.QueryRowContext(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatal("connect to isolated test database failed")
	}
	if !strings.HasPrefix(databaseName, "nyvp_") || !strings.HasSuffix(databaseName, "_test") {
		t.Fatal("refusing to mutate database: name must match nyvp_*_test")
	}
	schema := fmt.Sprintf("usage_test_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal("create isolated test schema failed")
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := admin.ExecContext(cleanupContext, `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Error("clean up usage test schema failed")
		}
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_, currentFile, _, _ := runtime.Caller(0)
	migrationPath := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "../../../../migrations/000003_usage_ledger.up.sql"))
	migration, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatal("read usage migration failed:", err)
	}
	if _, err := db.ExecContext(ctx, string(migration)); err != nil {
		t.Fatal("apply usage migration failed:", err)
	}
	return db
}

type integrationPolicies struct {
	limit int64
	err   error
}

type managedMetadata struct {
	metadata usage.LegacyRuleMetadata
	err      error
}

func (provider *managedMetadata) ResolveLegacyRuleUsageMetadata(context.Context, string, string) (usage.LegacyRuleMetadata, error) {
	return provider.metadata, provider.err
}

func TestPostgreSQLManagedReplayRetainsMultiplierAfterRuleChanges(t *testing.T) {
	db := isolatedUsageDatabase(t)
	repository := New(db)
	provider := &managedMetadata{metadata: usage.LegacyRuleMetadata{RuleID: "rule-one", CustomerID: "customer-one", EntryGroup: "entry-one", ExitGroup: "exit-one", Protocol: "tcp", EntryMultiplierMicros: 2 * usage.MultiplierScale, ExitMultiplierMicros: 1500000}}
	service := usage.NewService(repository, integrationPolicies{}, nil, usage.WithLegacyRuleMetadataProvider(provider))
	report := integrationReport(1)
	report.OccurredAt = report.OccurredAt.Add(123456789 * time.Nanosecond)
	report.PeriodEndedAt = report.OccurredAt
	got, err := service.Ingest(context.Background(), report)
	if err != nil || got.Event.ChargedBytes != 300 {
		t.Fatalf("trusted billing=%+v error=%v", got, err)
	}
	provider.metadata.EntryMultiplierMicros = 0
	provider.err = errors.New("rule is no longer applied")
	replayed, err := service.Ingest(context.Background(), report)
	if err != nil || !replayed.Replayed || replayed.Event.ChargedBytes != 300 || replayed.CustomerTotals.ChargedBytes != 300 {
		t.Fatalf("stored multiplier replay=%+v error=%v", replayed, err)
	}
	report.CustomerID = "forged"
	if _, err := service.Ingest(context.Background(), report); !errors.Is(err, usage.ErrIdempotencyConflict) {
		t.Fatalf("forged replay error=%v", err)
	}
}

func (policies integrationPolicies) UsagePolicy(_ context.Context, customerID string) (usage.CustomerPolicy, error) {
	if policies.err != nil {
		return usage.CustomerPolicy{}, policies.err
	}
	return usage.CustomerPolicy{CustomerID: customerID, TrafficLimitBytes: policies.limit}, nil
}

func integrationReport(sequence int64) usage.Report {
	ended := time.Date(2026, 10, 3, 1, int(sequence), 0, 0, time.UTC)
	return usage.Report{
		NodeID: "node-one", BootID: "boot-one", Sequence: sequence, CustomerID: "customer-one",
		RuleID: "rule-one", EntryGroupID: "entry-one", ExitGroupID: "exit-one", Protocol: "tcp", OccurredAt: ended,
		PeriodStartedAt: ended.Add(-time.Minute), PeriodEndedAt: ended, RuleActualBytes: 100,
		CustomerActualBytes: 100, EntryMultiplierMicros: usage.MultiplierScale, ExitMultiplierMicros: usage.MultiplierScale,
	}
}

func TestPostgreSQLLegacyReplayDoesNotDependOnCurrentRuleState(t *testing.T) {
	db := isolatedUsageDatabase(t)
	repository := New(db)
	ctx := context.Background()
	ended := time.Date(2026, 10, 6, 3, 0, 0, 123456789, time.UTC)
	eventReport := usage.Report{
		NodeID: "node-legacy", BootID: "boot-legacy", Sequence: 1, CustomerID: "customer-legacy", RuleID: "rule-legacy",
		EntryGroupID: "entry-legacy", Protocol: "tcp", OccurredAt: ended,
		PeriodStartedAt: ended.Add(-time.Minute), PeriodEndedAt: ended, RuleActualBytes: 42, CustomerActualBytes: 42,
		EntryMultiplierMicros: usage.MultiplierScale, ExitMultiplierMicros: usage.MultiplierScale,
	}
	service := usage.NewService(repository, integrationPolicies{}, func() time.Time { return ended })
	if _, err := service.Ingest(ctx, eventReport); err != nil {
		t.Fatal(err)
	}
	legacy := usage.Report{
		NodeID: "node-legacy", BootID: "boot-legacy", Sequence: 1, LegacyRuleID: "rule-legacy", RuleID: "rule-legacy",
		OccurredAt: ended, PeriodStartedAt: ended.Add(-time.Minute), PeriodEndedAt: ended,
		RuleActualBytes: 42, CustomerActualBytes: 42,
		EntryMultiplierMicros: usage.MultiplierScale, ExitMultiplierMicros: usage.MultiplierScale,
	}
	result, replayed, err := repository.ReplayLegacy(ctx, legacy)
	if err != nil || !replayed || !result.Replayed || result.Event.CustomerID != "customer-legacy" || result.CustomerTotals.ActualBytes != 42 {
		t.Fatalf("legacy replay result=%#v replayed=%v error=%v", result, replayed, err)
	}
	legacy.PeriodEndedAt = legacy.PeriodEndedAt.Add(time.Second)
	if _, replayed, err := repository.ReplayLegacy(ctx, legacy); !errors.Is(err, usage.ErrIdempotencyConflict) || replayed {
		t.Fatalf("changed legacy payload replayed=%v error=%v", replayed, err)
	}
}

func TestPostgreSQLIngestEnforcesReplayConflictAndStrictSequence(t *testing.T) {
	db := isolatedUsageDatabase(t)
	repository := New(db)
	service := usage.NewService(repository, integrationPolicies{limit: 150}, func() time.Time {
		return time.Date(2026, 10, 3, 3, 0, 0, 0, time.UTC)
	})
	ctx := context.Background()
	first, err := service.Ingest(ctx, integrationReport(1))
	if err != nil {
		t.Fatal(err)
	}
	if first.Replayed || first.CustomerTotals.ChargedBytes != 100 || first.Decision != nil {
		t.Fatalf("unexpected first ingest: %#v", first)
	}
	unavailablePolicy := usage.NewService(repository, integrationPolicies{err: errors.New("customer no longer available")}, time.Now)
	replay, err := unavailablePolicy.Ingest(ctx, integrationReport(1))
	if err != nil || !replay.Replayed || replay.CustomerTotals.ChargedBytes != 100 {
		t.Fatalf("same payload did not replay independently of current policy: result=%#v error=%v", replay, err)
	}
	conflict := integrationReport(1)
	conflict.RuleActualBytes++
	if _, err := unavailablePolicy.Ingest(ctx, conflict); !errors.Is(err, usage.ErrIdempotencyConflict) {
		t.Fatalf("same sequence with different payload error = %v", err)
	}
	if _, err := service.Ingest(ctx, integrationReport(3)); !errors.Is(err, usage.ErrSequenceGap) {
		t.Fatalf("sequence gap error = %v", err)
	}
	second, err := service.Ingest(ctx, integrationReport(2))
	if err != nil {
		t.Fatal(err)
	}
	if second.CustomerTotals.ChargedBytes != 200 || second.Decision == nil || second.Decision.Status != usage.EnforcementPending {
		t.Fatalf("quota projection not committed with second event: %#v", second)
	}
	newBoot := integrationReport(2)
	newBoot.BootID = "boot-two"
	if _, err := service.Ingest(ctx, newBoot); !errors.Is(err, usage.ErrSequenceGap) {
		t.Fatalf("new boot did not require sequence 1: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM usage_enforcement_decisions WHERE trigger_sequence=2`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM usage_events WHERE node_id='node-one' AND boot_id='boot-one' AND sequence=2`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Ingest(ctx, integrationReport(2)); !errors.Is(err, usage.ErrSequenceOutOfOrder) {
		t.Fatalf("missing lower event did not report out-of-order: %v", err)
	}
}

func TestPostgreSQLQueryFiltersPaginatesAndReturnsDecision(t *testing.T) {
	db := isolatedUsageDatabase(t)
	service := usage.NewService(New(db), integrationPolicies{limit: 150}, time.Now)
	ctx := context.Background()
	if _, err := service.Ingest(ctx, integrationReport(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Ingest(ctx, integrationReport(2)); err != nil {
		t.Fatal(err)
	}
	result, err := service.Query(ctx, usage.Query{Scope: usage.ScopeDeviceGroup, ScopeID: "exit-one", Page: 1, PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 || len(result.Items) != 1 || result.Totals.ChargedBytes != 200 || result.Totals.RuleActualBytes != 200 {
		t.Fatalf("unexpected device-group page: %#v", result)
	}
	if result.Items[0].Sequence != 2 || result.Items[0].Decision == nil || result.Items[0].Decision.Status != usage.EnforcementPending {
		t.Fatalf("newest decision was not joined: %#v", result.Items[0])
	}
	from := integrationReport(2).OccurredAt
	narrow, err := service.Query(ctx, usage.Query{Scope: usage.ScopeCustomer, ScopeID: "customer-one", From: &from, Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if narrow.Total != 1 || len(narrow.Items) != 1 || narrow.Items[0].Sequence != 2 {
		t.Fatalf("time/customer filter failed: %#v", narrow)
	}
}

func TestPostgreSQLPolicyFailureRollsBackEventCursorAndTotals(t *testing.T) {
	db := isolatedUsageDatabase(t)
	ctx := context.Background()
	repository := New(db)
	policyFailure := errors.New("policy unavailable")
	failing := usage.NewService(repository, integrationPolicies{err: policyFailure}, time.Now)
	if _, err := failing.Ingest(ctx, integrationReport(1)); !errors.Is(err, policyFailure) {
		t.Fatalf("policy error = %v", err)
	}
	working := usage.NewService(repository, integrationPolicies{}, time.Now)
	result, err := working.Ingest(ctx, integrationReport(1))
	if err != nil {
		t.Fatalf("rolled-back sequence could not be retried: %v", err)
	}
	if result.Replayed || result.CustomerTotals.ChargedBytes != 100 {
		t.Fatalf("policy failure leaked partial state: %#v", result)
	}
}

func TestPostgreSQLEnforcementApplyAndRevokeAreAcknowledgedAndIdempotent(t *testing.T) {
	db := isolatedUsageDatabase(t)
	service := usage.NewService(New(db), integrationPolicies{limit: 50}, func() time.Time {
		return time.Date(2026, 10, 3, 3, 0, 0, 0, time.UTC)
	})
	ctx := context.Background()
	ingested, err := service.Ingest(ctx, integrationReport(1))
	if err != nil || ingested.Decision == nil {
		t.Fatalf("ingest decision = %#v error=%v", ingested.Decision, err)
	}
	command, err := service.DesiredEnforcement(ctx, "node-one")
	if err != nil || command == nil || command.Action != agentv1.EnforcementDisableCustomer || command.Revision != 1 {
		t.Fatalf("disable command = %#v error=%v", command, err)
	}
	result := agentv1.EnforcementResultRequest{DecisionID: command.DecisionID, Action: command.Action, Revision: command.Revision, Status: agentv1.EnforcementResultSucceeded}
	applied, replayed, err := service.RecordEnforcementResult(ctx, "node-one", result)
	if err != nil || replayed || applied.Status != usage.EnforcementApplied || applied.Revision != 2 {
		t.Fatalf("apply acknowledgement = %#v replay=%v error=%v", applied, replayed, err)
	}
	_, replayed, err = service.RecordEnforcementResult(ctx, "node-one", result)
	if err != nil || !replayed {
		t.Fatalf("apply result replay = %v error=%v", replayed, err)
	}
	revoking, replayed, err := service.RequestRevoke(ctx, "admin-one", command.DecisionID, "revoke-key-one")
	if err != nil || replayed || revoking.Status != usage.EnforcementRevokePending || revoking.Action != agentv1.EnforcementEnableCustomer || revoking.Revision != 3 {
		t.Fatalf("revoke request = %#v replay=%v error=%v", revoking, replayed, err)
	}
	revokeCommand, err := service.DesiredEnforcement(ctx, "node-one")
	if err != nil || revokeCommand == nil || revokeCommand.Action != agentv1.EnforcementEnableCustomer || revokeCommand.Revision != 3 {
		t.Fatalf("enable command = %#v error=%v", revokeCommand, err)
	}
	revoked, _, err := service.RecordEnforcementResult(ctx, "node-one", agentv1.EnforcementResultRequest{
		DecisionID: revokeCommand.DecisionID, Action: revokeCommand.Action, Revision: revokeCommand.Revision, Status: agentv1.EnforcementResultSucceeded,
	})
	if err != nil || revoked.Status != usage.EnforcementRevoked || revoked.Revision != 4 {
		t.Fatalf("revoke acknowledgement = %#v error=%v", revoked, err)
	}
	replayedRevoke, replayed, err := service.RequestRevoke(ctx, "admin-one", command.DecisionID, "revoke-key-one")
	if err != nil || !replayed || replayedRevoke.Status != usage.EnforcementRevoked {
		t.Fatalf("revoke replay = %#v replay=%v error=%v", replayedRevoke, replayed, err)
	}
}
