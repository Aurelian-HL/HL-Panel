package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/usage"
)

func TestPostgreSQLUsageQueryKeepsTotalsAndItemsInOneSnapshot(t *testing.T) {
	db := isolatedUsageDatabase(t)
	repository := New(db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	service := usage.NewService(repository, integrationPolicies{}, nil)
	if _, err := service.Ingest(ctx, integrationReport(1)); err != nil {
		t.Fatal(err)
	}

	// Only the detail query touches this table. Hold it until the aggregate
	// query has finished, then commit a second ledger event in another session.
	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if _, err = blocker.ExecContext(ctx, `LOCK TABLE usage_enforcement_decisions IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	type queryResult struct {
		result usage.QueryResult
		err    error
	}
	done := make(chan queryResult, 1)
	go func() {
		result, err := repository.Query(ctx, usage.Query{Scope: usage.ScopeRule, ScopeID: "rule-one", Page: 1, PageSize: 100})
		done <- queryResult{result, err}
	}()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting bool
		if err := blocker.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_locks WHERE relation='usage_enforcement_decisions'::regclass
			AND mode='AccessShareLock' AND NOT granted)`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case early := <-done:
			t.Fatalf("query returned before the detail barrier: %v", early.err)
		case <-ctx.Done():
			t.Fatal("detail query did not reach the barrier")
		case <-tick.C:
		}
	}
	// A valid committed ledger row is enough to reproduce a report arriving
	// between the two reads; it uses only this test's isolated schema.
	if _, err = db.ExecContext(ctx, `INSERT INTO usage_events SELECT
		node_id,boot_id,2,payload_sha256,customer_id,rule_id,entry_group_id,exit_group_id,protocol,
		occurred_at+interval '1 minute',period_started_at+interval '1 minute',period_ended_at+interval '1 minute',
		rule_actual_bytes,customer_actual_bytes,charged_bytes,entry_multiplier_micros,exit_multiplier_micros,received_at
		FROM usage_events WHERE sequence=1`); err != nil {
		t.Fatal(err)
	}
	if err = blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.result.Total != 1 || len(got.result.Items) != 1 || got.result.Totals.ChargedBytes != 100 || got.result.Items[0].Sequence != 1 {
		t.Fatalf("usage response mixed database snapshots: total=%d items=%d charged=%d", got.result.Total, len(got.result.Items), got.result.Totals.ChargedBytes)
	}
	fresh, err := repository.Query(ctx, usage.Query{Scope: usage.ScopeRule, ScopeID: "rule-one", Page: 1, PageSize: 100})
	if err != nil || fresh.Total != 2 || len(fresh.Items) != 2 || fresh.Totals.ChargedBytes != 200 {
		t.Fatalf("next request failed to observe the committed event: %+v %v", fresh, err)
	}
}
