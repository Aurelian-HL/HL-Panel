package usage

import (
	"context"
	"errors"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"io"
	"log/slog"
	"testing"
	"time"
)

type quotaStore struct {
	total  int64
	fail   bool
	calls  int
	cancel context.CancelFunc
}

func (q *quotaStore) ListForwardingRules(context.Context) ([]forwarding.Rule, error) {
	return []forwarding.Rule{{ID: "rule-one", TrafficLimitBytes: 10}}, nil
}
func (q *quotaStore) SyncRuleTraffic(_ context.Context, id string, total int64, _ time.Time) error {
	q.calls++
	if q.fail {
		return errors.New("isolated persistence failure")
	}
	if id != "rule-one" {
		return errors.New("wrong rule")
	}
	q.total = total
	if q.cancel != nil {
		q.cancel()
	}
	return nil
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
