package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/usage"
)

func TestRuleChargedBytesRetainsLedgerBoundariesAndSkipsDetails(t *testing.T) {
	db := isolatedUsageDatabase(t)
	repository := New(db)
	ctx := context.Background()
	service := usage.NewService(repository, integrationPolicies{}, nil)
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, 0)
	for i, at := range []time.Time{from.Add(-time.Second), from, to.Add(-time.Second), to} {
		r := integrationReport(int64(i + 1))
		r.OccurredAt, r.PeriodEndedAt = at, at
		r.PeriodStartedAt = at.Add(-time.Second)
		if _, err := service.Ingest(ctx, r); err != nil {
			t.Fatal(err)
		}
		if replay, err := service.Ingest(ctx, r); err != nil || !replay.Replayed {
			t.Fatal("ledger replay failed", err)
		}
	}
	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if _, err := blocker.ExecContext(ctx, `LOCK TABLE usage_enforcement_decisions IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for _, tc := range []struct {
		id       string
		from, to *time.Time
		want     int64
	}{
		{"rule-one", nil, nil, 400}, {"rule-one", &from, &to, 200}, {"missing-rule", nil, nil, 0},
	} {
		total, err := repository.RuleChargedBytes(bounded, usage.Query{Scope: usage.ScopeRule, ScopeID: tc.id, From: tc.from, To: tc.to})
		if err != nil || total != tc.want {
			t.Fatalf("total=%d want=%d err=%v", total, tc.want, err)
		}
	}
	if _, err := repository.RuleChargedBytes(bounded, usage.Query{Scope: usage.ScopeSite}); err == nil {
		t.Fatal("invalid rule scope accepted")
	}
	if err := blocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RuleChargedBytes(ctx, usage.Query{Scope: usage.ScopeRule, ScopeID: "rule-one"}); err == nil {
		t.Fatal("database failure became a zero quota")
	}
}
