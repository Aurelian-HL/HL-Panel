package memoryrepo

import (
	"context"
	"fmt"
	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"time"
)

func preserveRuleTraffic(item *forwarding.Rule, previous forwarding.Rule) {
	item.TrafficUsedBytes = previous.TrafficUsedBytes
	item.TrafficUsagePeriod = previous.TrafficUsagePeriod
	if previous.TrafficQuotaMonthly != item.TrafficQuotaMonthly {
		// The next reconciliation queries the new accounting scope. Ordinary
		// edits and imports must never erase already charged traffic.
		item.TrafficUsedBytes = 0
		item.TrafficUsagePeriod = ""
	}
}

// SyncRuleTraffic consumes an absolute ledger total. Retries and concurrent
// reports cannot double charge, reduce the total, or overwrite an edited limit.
func (s *Store) SyncRuleTraffic(_ context.Context, id string, total int64, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.syncRuleTrafficLocked(id, total, at)
}

func (s *Store) ApplyRuleTraffic(_ context.Context, projection forwarding.RuleTrafficProjection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if projection.ExpectedRevision < 1 || projection.TotalBytes < 0 || projection.At.IsZero() {
		return faults.ErrValidation
	}
	rule, ok := s.forwardRules[projection.RuleID]
	if !ok {
		return faults.ErrNotFound
	}
	if rule.Revision != projection.ExpectedRevision || rule.TrafficQuotaMonthly != projection.Monthly {
		// The periodic reconciliation will query the current scope again. In
		// particular, switching modes twice must not accept an older query.
		return nil
	}
	return s.syncRuleTrafficLocked(projection.RuleID, projection.TotalBytes, projection.At)
}

func (s *Store) syncRuleTrafficLocked(id string, total int64, at time.Time) error {
	rule, ok := s.forwardRules[id]
	if !ok {
		return faults.ErrNotFound
	}
	if total < 0 {
		return faults.ErrValidation
	}
	period := ""
	periodChanged := false
	if rule.TrafficQuotaMonthly {
		period = forwarding.TrafficQuotaPeriod(at)
		if rule.TrafficUsagePeriod > period {
			return nil
		}
		periodChanged = rule.TrafficUsagePeriod != period
	}
	usageChanged := periodChanged || total > rule.TrafficUsedBytes
	if !usageChanged {
		return nil
	}
	previous := rule
	before := rule.TrafficLimitBytes > 0 && rule.TrafficUsedBytes >= rule.TrafficLimitBytes
	if rule.TrafficQuotaMonthly && periodChanged {
		rule.TrafficUsagePeriod = period
	}
	rule.TrafficUsedBytes = total
	exhausted := rule.TrafficLimitBytes > 0 && total >= rule.TrafficLimitBytes
	quotaStateChanged := exhausted != before
	if usageChanged {
		rule.UpdatedAt = at
	}
	if quotaStateChanged {
		rule.Revision++
		rule.Status = forwarding.DeriveStatus(rule, forwarding.CustomerAvailability{Enabled: true}, at)
	}
	s.forwardRules[id] = rule
	if quotaStateChanged || periodChanged {
		action := "forwarding.quota_exhausted"
		if periodChanged && !exhausted {
			action = "forwarding.quota_period_reset"
		} else if periodChanged {
			action = "forwarding.quota_period_changed"
		}
		event, err := audit.NewEvent(at, "system", "rule-traffic-quota", action, "forwarding_rule", id, "succeeded", map[string]any{"traffic_limit_bytes": rule.TrafficLimitBytes, "traffic_used_bytes": total, "traffic_quota_monthly": rule.TrafficQuotaMonthly, "traffic_usage_period": rule.TrafficUsagePeriod})
		if err != nil {
			s.forwardRules[id] = previous
			return err
		}
		if quotaStateChanged {
			if err := s.recompileForwardingGroupsLocked([]string{rule.EntryGroupID}, at); err != nil {
				s.forwardRules[id] = previous
				return fmt.Errorf("recompile quota state: %w", err)
			}
		}
		s.appendAuditLocked(event)
	}
	return nil
}
