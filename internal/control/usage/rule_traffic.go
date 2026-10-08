package usage

import (
	"context"
	"errors"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"log/slog"
	"time"
)

type RuleTrafficRepository interface {
	ListForwardingRules(context.Context) ([]forwarding.Rule, error)
	SyncRuleTraffic(context.Context, string, int64, time.Time) error
}

func WithRuleTrafficRepository(r RuleTrafficRepository) Option {
	return func(s *Service) { s.ruleTraffic = r }
}
func (s *Service) syncRuleTraffic(ctx context.Context, id string) error {
	if s.ruleTraffic == nil {
		return nil
	}
	totals, err := s.repository.Query(ctx, Query{Scope: ScopeRule, ScopeID: id, Page: 1, PageSize: 1})
	if err != nil {
		return err
	}
	err = s.ruleTraffic.SyncRuleTraffic(ctx, id, totals.Totals.ChargedBytes, s.now().UTC())
	if errors.Is(err, faults.ErrNotFound) {
		return nil
	}
	return err
}
func (s *Service) afterIngest(ctx context.Context, result IngestResult, err error, id string) (IngestResult, error) {
	if err != nil {
		return result, err
	}
	return result, s.syncRuleTraffic(ctx, id)
}

// A separate durable ledger and control snapshot cannot share a transaction.
// Reconcile at startup and periodically so a crash after ledger commit or a
// failed generation compile is retried even without another usage report.
func (s *Service) RunRuleTraffic(ctx context.Context, logger *slog.Logger) error {
	if s.ruleTraffic == nil {
		return nil
	}
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		bounded, cancel := context.WithTimeout(ctx, 20*time.Second)
		rules, err := s.ruleTraffic.ListForwardingRules(bounded)
		if err == nil {
			for _, r := range rules {
				if bounded.Err() != nil {
					break
				}
				if r.TrafficLimitBytes == 0 {
					continue
				}
				if syncErr := s.syncRuleTraffic(bounded, r.ID); syncErr != nil && ctx.Err() == nil {
					logger.Warn("rule traffic reconciliation failed", "rule_id", r.ID, "error", syncErr)
				}
			}
		}
		cancel()
		if err != nil && ctx.Err() == nil {
			logger.Warn("rule traffic reconciliation failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
