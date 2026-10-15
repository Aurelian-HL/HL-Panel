package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/usage"
)

func assertQueryTotalsMatchLedger(t *testing.T, db *sql.DB, q usage.Query) {
	t.Helper()
	ctx := context.Background()
	fast, args := queryTotalsSQL(q)
	where, rawArgs := queryFilter(q)
	raw := `SELECT count(*),COALESCE(sum(rule_actual_bytes),0)::bigint,COALESCE(sum(customer_actual_bytes),0)::bigint,COALESCE(sum(charged_bytes),0)::bigint FROM usage_events e` + where
	var actual, expected [4]int64
	if err := db.QueryRowContext(ctx, fast, args...).Scan(&actual[0], &actual[1], &actual[2], &actual[3]); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, raw, rawArgs...).Scan(&expected[0], &expected[1], &expected[2], &expected[3]); err != nil {
		t.Fatal(err)
	}
	if actual != expected {
		t.Fatalf("scope=%s query=%+v actual=%v want=%v", q.Scope, q, actual, expected)
	}
	result, err := New(db).Query(ctx, q)
	if err != nil || int64(result.Total) != expected[0] || result.Totals.RuleActualBytes != expected[1] || result.Totals.CustomerActualBytes != expected[2] || result.Totals.ChargedBytes != expected[3] {
		t.Fatalf("page disagrees with aggregate: %+v %v", result, err)
	}
}

const queryTotalsFixtureSQL = `INSERT INTO usage_ingest_cursors VALUES('node','boot',100,now());
INSERT INTO usage_events
SELECT 'node','boot',n,repeat('a',64),'customer-'||(n%2),'rule-'||(n%3),
 'group-'||(n%2), CASE WHEN n%3=0 THEN 'group-'||(n%2) ELSE 'exit' END,'tcp',
 '2026-01-31 23:59:30+00'::timestamptz+n*interval '1 second',now()-interval '1 second',now(),n,n*2,n*3,1000000,1000000,now()
FROM generate_series(1,100) n`

func TestQueryTotalsExactScopesMonthsMutationsAndRollback(t *testing.T) {
	db := isolatedUsageDatabase(t)
	if _, err := db.Exec(queryTotalsFixtureSQL); err != nil {
		t.Fatal(err)
	}
	january := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	february, march := january.AddDate(0, 1, 0), january.AddDate(0, 2, 0)
	partial := february.Add(20 * time.Second)
	check := func() {
		for _, scope := range []usage.Scope{usage.ScopeSite, usage.ScopeCustomer, usage.ScopeRule, usage.ScopeDeviceGroup} {
			id := map[usage.Scope]string{usage.ScopeCustomer: "customer-0", usage.ScopeRule: "rule-0", usage.ScopeDeviceGroup: "group-0"}[scope]
			for _, bounds := range [][2]*time.Time{{nil, nil}, {&january, &february}, {&february, &march}, {&january, &march}, {&february, &partial}, {&partial, nil}} {
				assertQueryTotalsMatchLedger(t, db, usage.Query{Scope: scope, ScopeID: id, From: bounds[0], To: bounds[1], Page: 1, PageSize: 5})
			}
		}
	}
	check()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`DELETE FROM usage_events`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	check()
	if _, err = db.Exec(`UPDATE usage_events SET occurred_at=occurred_at+interval '1 month',rule_id='rule-0',charged_bytes=charged_bytes+10 WHERE sequence%2=0; DELETE FROM usage_events WHERE sequence%3=0`); err != nil {
		t.Fatal(err)
	}
	check()
	// Hold the ledger exclusively: the unbounded/month aggregate must not
	// read it. Pagination still legitimately reads the requested ledger page.
	tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`LOCK TABLE usage_events IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	bounded, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, q := range []usage.Query{{Scope: usage.ScopeSite}, {Scope: usage.ScopeDeviceGroup, ScopeID: "group-0", From: &january, To: &march}} {
		statement, args := queryTotalsSQL(q)
		var totals [4]int64
		if err := db.QueryRowContext(bounded, statement, args...).Scan(&totals[0], &totals[1], &totals[2], &totals[3]); err != nil {
			t.Fatal("summary touched ledger", err)
		}
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`TRUNCATE usage_events CASCADE`); err != nil {
		t.Fatal(err)
	}
	check()
}

func TestQueryTotalsMillionEventMeasurement(t *testing.T) {
	if os.Getenv("HL_TEST_MILLION_USAGE") != "true" {
		t.Skip("explicit local measurement only")
	}
	db := isolatedUsageDatabase(t)
	// One million ledger records with 300 distinct reporting tuples.
	started := time.Now()
	_, err := db.Exec(`INSERT INTO usage_ingest_cursors SELECT 'node-'||n,'boot',1000000,now() FROM generate_series(0,2) n;
    INSERT INTO usage_events SELECT 'node-'||(n%3),'boot',n,repeat('a',64),'customer-'||(n%20),'rule-'||(n%100),'entry','','tcp',now(),now()-interval '1 second',now(),10,20,30,1000000,1000000,now() FROM generate_series(1,1000000) n`)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("million event insertion (both projections): %s", time.Since(started))
	if _, err = db.Exec(`ANALYZE usage_events; ANALYZE usage_query_totals`); err != nil {
		t.Fatal(err)
	}
	q := usage.Query{Scope: usage.ScopeSite, Page: 1, PageSize: 20}
	assertQueryTotalsMatchLedger(t, db, q)
	fast, args := queryTotalsSQL(q)
	for _, measurement := range []struct {
		name, statement string
		args            []any
	}{{"ledger", "SELECT count(*),sum(rule_actual_bytes),sum(customer_actual_bytes),sum(charged_bytes) FROM usage_events", nil}, {"projection", fast, args}} {
		var plans []byte
		if err = db.QueryRow(`EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) `+measurement.statement, measurement.args...).Scan(&plans); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s plan: %s", measurement.name, plans)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM usage_query_totals`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	t.Log(fmt.Sprintf("1000000 ledger records => %d exact aggregate rows", count))
}
