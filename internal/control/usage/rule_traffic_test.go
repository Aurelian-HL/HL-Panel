package usage

import (
	"context"
	"errors"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"io"
	"log/slog"
	"testing"
	"time"
)

type quotaStore struct {
	total      int64
	fail       bool
	calls      int
	monthly    bool
	unlimited  bool
	cancel     context.CancelFunc
	listCalls  int
	getCalls   int
	rules      []forwarding.Rule
	projection forwarding.RuleTrafficProjection
}

func (q *quotaStore) ListForwardingRules(context.Context) ([]forwarding.Rule, error) {
	q.listCalls++
	return q.currentRules(), nil
}
func (q *quotaStore) currentRules() []forwarding.Rule {
	if q.rules != nil {
		return q.rules
	}
	limit := int64(10)
	if q.unlimited {
		limit = 0
	}
	return []forwarding.Rule{{ID: "rule-one", Revision: 1, TrafficLimitBytes: limit, TrafficQuotaMonthly: q.monthly}}
}
func (q *quotaStore) ForwardingRule(_ context.Context, id string) (forwarding.Rule, error) {
	q.getCalls++
	for _, rule := range q.currentRules() {
		if rule.ID == id {
			return rule, nil
		}
	}
	return forwarding.Rule{}, faults.ErrNotFound
}
func (q *quotaStore) ApplyRuleTraffic(_ context.Context, projection forwarding.RuleTrafficProjection) error {
	q.calls++
	if q.fail {
		return errors.New("isolated persistence failure")
	}
	if q.rules == nil && projection.RuleID != "rule-one" {
		return errors.New("wrong rule")
	}
	q.total = projection.TotalBytes
	q.projection = projection
	if q.cancel != nil {
		q.cancel()
	}
	return nil
}

func TestRuleTrafficReconciliationLoadsInventoryOnce(t *testing.T) {
	repository := &stubRepository{queryResult: QueryResult{Totals: Totals{ChargedBytes: 15}}}
	quota := &quotaStore{rules: []forwarding.Rule{
		{ID: "rule-one", Revision: 1, TrafficLimitBytes: 10},
		{ID: "rule-two", Revision: 2, TrafficLimitBytes: 20},
		{ID: "rule-three", Revision: 3, TrafficLimitBytes: 30},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	quota.cancel = func() {
		if quota.calls == len(quota.rules) {
			cancel()
		}
	}
	service := NewService(repository, stubPolicies{}, nil, WithRuleTrafficRepository(quota))
	if err := service.RunRuleTraffic(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if quota.listCalls != 1 || quota.getCalls != 0 || quota.calls != 3 {
		t.Fatalf("inventory repeatedly loaded: lists=%d gets=%d projections=%d", quota.listCalls, quota.getCalls, quota.calls)
	}
}

func TestRuleTrafficDoesNotRewriteUnchangedTotals(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for _, monthly := range []bool{false, true} {
		quota := &quotaStore{rules: []forwarding.Rule{{ID: "rule-one", Revision: 3, TrafficQuotaMonthly: monthly, TrafficUsagePeriod: "2026-10", TrafficUsedBytes: 15}}}
		repository := &stubRepository{queryResult: QueryResult{Totals: Totals{ChargedBytes: 15}}}
		service := NewService(repository, stubPolicies{}, func() time.Time { return now }, WithRuleTrafficRepository(quota))
		if err := service.syncRuleTraffic(context.Background(), "rule-one"); err != nil {
			t.Fatal(err)
		}
		if quota.calls != 0 || quota.getCalls != 1 || quota.listCalls != 0 {
			t.Fatalf("unchanged total caused a snapshot rewrite: monthly=%t calls=%d", monthly, quota.calls)
		}
	}
}
func TestUsageCommitFailureIsRecoveredByReconciliation(t *testing.T) {
	repository := &stubRepository{queryResult: QueryResult{Totals: Totals{ChargedBytes: 15}}}
	quota := &quotaStore{fail: true}
	service := NewService(repository, stubPolicies{policy: CustomerPolicy{CustomerID: "customer-one"}}, nil, WithRuleTrafficRepository(quota))
	if _, err := service.Ingest(context.Background(), testReport()); err == nil {
		t.Fatal("failed projection was hidden")
	}
	if len(repository.events) != 1 {
		t.Fatal("ledger did not commit")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	quota.fail = false
	quota.cancel = cancel
	if err := service.RunRuleTraffic(ctx, slog.New(slog.NewTextHandler(io.Discard, nil))); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if quota.total != 15 || quota.calls != 2 {
		t.Fatal("startup did not recover quota", quota)
	}
}

func TestMonthlyRuleTrafficUsesCurrentUTCMonth(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	repository := &stubRepository{queryResult: QueryResult{Totals: Totals{ChargedBytes: 15}}}
	quota := &quotaStore{monthly: true}
	service := NewService(repository, stubPolicies{policy: CustomerPolicy{CustomerID: "customer-one"}}, func() time.Time { return now }, WithRuleTrafficRepository(quota))
	if _, err := service.Ingest(context.Background(), testReport()); err != nil {
		t.Fatal(err)
	}
	if repository.query.From == nil || repository.query.To == nil {
		t.Fatal("monthly rule did not constrain the usage query")
	}
	wantFrom := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	wantTo := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if !repository.query.From.Equal(wantFrom) || !repository.query.To.Equal(wantTo) {
		t.Fatalf("usage range = %s..%s, want %s..%s", repository.query.From, repository.query.To, wantFrom, wantTo)
	}
	if !quota.projection.Monthly || quota.projection.ExpectedRevision != 1 || !quota.projection.At.Equal(now) {
		t.Fatal("projection lost query scope or revision")
	}
}

func TestMonthlyUnlimitedRuleTrafficIsReconciled(t *testing.T) {
	repository := &stubRepository{queryResult: QueryResult{Totals: Totals{ChargedBytes: 15}}}
	quota := &quotaStore{monthly: true, unlimited: true}
	ctx, cancel := context.WithCancel(context.Background())
	quota.cancel = cancel
	service := NewService(repository, stubPolicies{policy: CustomerPolicy{CustomerID: "customer-one"}}, nil, WithRuleTrafficRepository(quota))
	if err := service.RunRuleTraffic(ctx, slog.New(slog.NewTextHandler(io.Discard, nil))); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if quota.calls != 1 || quota.total != 15 {
		t.Fatalf("monthly unlimited rule was not reconciled: calls=%d total=%d", quota.calls, quota.total)
	}
}
