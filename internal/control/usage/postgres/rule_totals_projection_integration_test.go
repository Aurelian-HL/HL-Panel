package postgres

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/usage"
)

func TestRuleTotalsMaintainTransactionsBulkEditsAndConcurrentReports(t *testing.T) {
	db := isolatedUsageDatabase(t)
	ctx := context.Background()
	repository := New(db)
	insert := `WITH cursor AS (INSERT INTO usage_ingest_cursors VALUES($1,'boot',1,now()) RETURNING node_id)
 INSERT INTO usage_events SELECT node_id,'boot',1,repeat('a',64),'customer','rule','entry','','tcp',$2::timestamptz,$2::timestamptz-interval '1 second',$2::timestamptz,10,10,10,1000000,1000000,$2::timestamptz FROM cursor`
	january := time.Date(2026, 1, 31, 23, 59, 59, 0, time.UTC)
	february := january.Add(time.Second)
	read := func(want int64, from, to *time.Time) {
		t.Helper()
		got, err := repository.RuleChargedBytes(ctx, usage.Query{Scope: usage.ScopeRule, ScopeID: "rule", From: from, To: to})
		if err != nil || got != want {
			t.Fatalf("total=%d want=%d err=%v", got, want, err)
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, insert, "rolled-back", january); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	read(0, nil, nil)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := db.ExecContext(ctx, insert, fmt.Sprintf("node-%d", i), january)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	read(160, nil, nil)
	// Move every event across the UTC boundary and change charged usage in one statement.
	if _, err = db.ExecContext(ctx, `UPDATE usage_events SET charged_bytes=20,occurred_at=$1`, february); err != nil {
		t.Fatal(err)
	}
	read(320, nil, nil)
	janStart := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	febStart := janStart.AddDate(0, 1, 0)
	marStart := febStart.AddDate(0, 1, 0)
	read(0, &janStart, &febStart)
	read(320, &febStart, &marStart)
	// Arbitrary sub-month reports still retain ledger boundaries.
	until := february.Add(time.Second)
	read(320, &february, &until)
	if _, err = db.ExecContext(ctx, `DELETE FROM usage_events WHERE node_id='node-0'`); err != nil {
		t.Fatal(err)
	}
	read(300, nil, nil)
	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if _, err = blocker.ExecContext(ctx, `LOCK TABLE usage_events IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	bounded, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if total, err := repository.RuleChargedBytes(bounded, usage.Query{Scope: usage.ScopeRule, ScopeID: "rule"}); err != nil || total != 300 {
		t.Fatal("cumulative query still touches ledger", total, err)
	}
	if total, err := repository.RuleChargedBytes(bounded, usage.Query{Scope: usage.ScopeRule, ScopeID: "rule", From: &febStart, To: &marStart}); err != nil || total != 300 {
		t.Fatal("monthly query still touches ledger", total, err)
	}
	if err = blocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `TRUNCATE usage_events CASCADE`); err != nil {
		t.Fatal(err)
	}
	read(0, nil, nil)
	// Preserve overflow errors rather than silently resetting an exhausted quota.
	if _, err = db.ExecContext(ctx, `INSERT INTO usage_rule_totals VALUES('rule','',9223372036854775808)`); err != nil {
		t.Fatal(err)
	}
	if _, err = repository.RuleChargedBytes(ctx, usage.Query{Scope: usage.ScopeRule, ScopeID: "rule"}); err == nil {
		t.Fatal("overflow became valid usage")
	}
}
