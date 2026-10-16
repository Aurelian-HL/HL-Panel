package usage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/forwarding"
)

type lightweightQuotaStore struct {
	quotaStore
	stateReads int
	listReads  int
}

type aggregateQuotaRepository struct {
	stubRepository
	query Query
	err   error
}

func (r *aggregateQuotaRepository) RuleChargedBytes(_ context.Context, q Query) (int64, error) {
	r.query = q
	return 15, r.err
}

func TestQuotaUsesAggregateReaderAndPropagatesFailure(t *testing.T) {
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	r := &aggregateQuotaRepository{}
	q := &quotaStore{monthly: true}
	clock := now
	s := NewService(r, stubPolicies{}, func() time.Time { return clock }, WithRuleTrafficRepository(q))
	if err := s.syncRuleTraffic(context.Background(), "rule-one"); err != nil {
		t.Fatal(err)
	}
	if q.total != 15 || r.query.From == nil || r.query.To == nil || r.stubRepository.query.Scope != "" {
		t.Fatal("quota fetched detail page or lost monthly bounds")
	}
	r.err = errors.New("isolated aggregate failure")
	clock = now.Add(ruleTrafficSyncInterval)
	if err := s.syncRuleTraffic(context.Background(), "rule-one"); !errors.Is(err, r.err) || q.calls != 1 {
		t.Fatal("failed aggregate changed quota", err)
	}
}

func (q *lightweightQuotaStore) RuleTrafficState(_ context.Context, _ string) (forwarding.RuleTrafficState, error) {
	q.stateReads++
	return forwarding.TrafficState(q.currentRules()[0]), nil
}
func (q *lightweightQuotaStore) ListRuleTrafficStates(context.Context) ([]forwarding.RuleTrafficState, error) {
	q.listReads++
	return []forwarding.RuleTrafficState{forwarding.TrafficState(q.currentRules()[0])}, nil
}

func TestUsageAndReconciliationSkipDeploymentProjection(t *testing.T) {
	q := &lightweightQuotaStore{}
	repository := &stubRepository{queryResult: QueryResult{Totals: Totals{ChargedBytes: 15}}}
	s := NewService(repository, stubPolicies{}, nil, WithRuleTrafficRepository(q))
	if err := s.syncRuleTraffic(context.Background(), "rule-one"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.cancel = cancel
	if err := s.RunRuleTraffic(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if q.getCalls != 0 || q.listCalls != 0 || q.stateReads != 1 || q.listReads != 1 || q.calls != 2 || q.projection.TotalBytes != 15 {
		t.Fatalf("expensive deployment projection used or accounting changed: %+v", q)
	}
}
