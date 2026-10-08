package memoryrepo

import (
	"context"
	"fmt"
	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"time"
)

// SyncRuleTraffic consumes an absolute ledger total. Retries and concurrent
// reports cannot double charge, reduce the total, or overwrite an edited limit.
func (s *Store) SyncRuleTraffic(_ context.Context, id string, total int64, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rule, ok := s.forwardRules[id]
	if !ok {
		return faults.ErrNotFound
	}
	if total < 0 {
		return faults.ErrValidation
	}
	if total <= rule.TrafficUsedBytes {
		return nil
	}
	previous := rule
	before := rule.TrafficLimitBytes > 0 && rule.TrafficUsedBytes >= rule.TrafficLimitBytes
	rule.TrafficUsedBytes = total
	exhausted := rule.TrafficLimitBytes > 0 && total >= rule.TrafficLimitBytes
	if exhausted && !before {
		rule.Revision++
		rule.UpdatedAt = at
		rule.Status = forwarding.DeriveStatus(rule, forwarding.CustomerAvailability{Enabled: true}, at)
	}
	s.forwardRules[id] = rule
	if exhausted && !before {
		event, err := audit.NewEvent(at, "system", "rule-traffic-quota", "forwarding.quota_exhausted", "forwarding_rule", id, "succeeded", map[string]any{"traffic_limit_bytes": rule.TrafficLimitBytes, "traffic_used_bytes": total})
		if err != nil {
			s.forwardRules[id] = previous
			return err
		}
		if err := s.recompileForwardingGroupsLocked([]string{rule.EntryGroupID}, at); err != nil {
			s.forwardRules[id] = previous
			return fmt.Errorf("recompile quota pause: %w", err)
		}
		s.appendAuditLocked(event)
	}
	return nil
}
