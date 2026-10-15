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
	ForwardingRule(context.Context, string) (forwarding.Rule, error)
	ApplyRuleTraffic(context.Context, forwarding.RuleTrafficProjection) error
}

// RuleTrafficStateReader avoids deployment-status work during accounting.
// Older repositories remain supported through the full-rule fallback.
type RuleTrafficStateReader interface {
	RuleTrafficState(context.Context, string) (forwarding.RuleTrafficState, error)
	ListRuleTrafficStates(context.Context) ([]forwarding.RuleTrafficState, error)
}

func WithRuleTrafficRepository(r RuleTrafficRepository) Option {
	return func(s *Service) { s.ruleTraffic = r }
}
func (s *Service) syncRuleTraffic(ctx context.Context, id string) error {
	if s.ruleTraffic == nil {
		return nil
	}
	var rule forwarding.RuleTrafficState
	var err error
	if reader, ok := s.ruleTraffic.(RuleTrafficStateReader); ok {
		rule, err = reader.RuleTrafficState(ctx, id)
	} else {
		var full forwarding.Rule
		full, err = s.ruleTraffic.ForwardingRule(ctx, id)
		rule = forwarding.TrafficState(full)
	}
	if errors.Is(err, faults.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.syncRuleTrafficProjection(ctx, rule)
}

func (s *Service) syncRuleTrafficProjection(ctx context.Context, rule forwarding.RuleTrafficState) error {
	now := s.now().UTC()
	query := Query{Scope: ScopeRule, ScopeID: rule.ID, Page: 1, PageSize: 1}
	if rule.TrafficQuotaMonthly {
		from, to := forwarding.TrafficQuotaPeriodBounds(now)
		query.From, query.To = &from, &to
	}
	chargedBytes, err := s.ruleChargedBytes(ctx, query)
	if err != nil {
		return err
	}
	// Avoid rewriting the complete control snapshot for every idle rule every
	// five seconds. A new monthly period still needs to commit a zero total.
	if chargedBytes <= rule.TrafficUsedBytes && (!rule.TrafficQuotaMonthly || rule.TrafficUsagePeriod == forwarding.TrafficQuotaPeriod(now)) {
		return nil
	}
	err = s.ruleTraffic.ApplyRuleTraffic(ctx, forwarding.RuleTrafficProjection{
		RuleID: rule.ID, ExpectedRevision: rule.Revision, Monthly: rule.TrafficQuotaMonthly,
		TotalBytes: chargedBytes, At: now,
	})
	if errors.Is(err, faults.ErrNotFound) {
		return nil
	}
	return err
}

func (s *Service) ruleChargedBytes(ctx context.Context, query Query) (int64, error) {
	if reader, ok := s.repository.(RuleChargedBytesReader); ok {
		return reader.RuleChargedBytes(ctx, query)
	}
	result, err := s.repository.Query(ctx, query)
	return result.Totals.ChargedBytes, err
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
	if logger == nil {
		logger = slog.Default()
	}
	// Usage reports reconcile immediately in afterIngest. This loop is the
	// crash-recovery and month-boundary safety net; scanning every five seconds
	// repeatedly decodes the full control snapshot for every limited rule.
	// Thirty seconds keeps recovery and monthly reset bounded without turning
	// the database into a permanent polling workload.
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		bounded, cancel := context.WithTimeout(ctx, 20*time.Second)
		rules, err := s.ruleTrafficStates(bounded)
		if err == nil {
			for _, r := range rules {
				if bounded.Err() != nil {
					break
				}
				// Monthly unlimited rules still need reconciliation so a new UTC
				// month resets their usage period and audit state.
				if r.TrafficLimitBytes == 0 && !r.TrafficQuotaMonthly {
					continue
				}
				if syncErr := s.syncRuleTrafficProjection(bounded, r); syncErr != nil && ctx.Err() == nil {
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

func (s *Service) ruleTrafficStates(ctx context.Context) ([]forwarding.RuleTrafficState, error) {
	if reader, ok := s.ruleTraffic.(RuleTrafficStateReader); ok {
		return reader.ListRuleTrafficStates(ctx)
	}
	rules, err := s.ruleTraffic.ListForwardingRules(ctx)
	if err != nil {
		return nil, err
	}
	states := make([]forwarding.RuleTrafficState, len(rules))
	for i, rule := range rules {
		states[i] = forwarding.TrafficState(rule)
	}
	return states, nil
}
