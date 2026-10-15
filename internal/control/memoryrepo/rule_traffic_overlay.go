package memoryrepo

import (
	"time"

	"github.com/hongle/hl-panel/internal/control/forwarding"
)

// RuleTrafficOverlay is a durable high-frequency counter projection. Lifecycle
// transitions (month changes and quota crossings) must use ApplyRuleTraffic.
type RuleTrafficOverlay struct {
	State     forwarding.RuleTrafficState
	UpdatedAt time.Time
}

func MergeRuleTrafficOverlay(rule forwarding.RuleTrafficState, overlay RuleTrafficOverlay) forwarding.RuleTrafficState {
	value := overlay.State
	if value.Revision == rule.Revision && value.TrafficQuotaMonthly == rule.TrafficQuotaMonthly &&
		value.TrafficUsagePeriod == rule.TrafficUsagePeriod && value.TrafficUsedBytes > rule.TrafficUsedBytes &&
		(rule.TrafficLimitBytes > 0 && value.TrafficUsedBytes >= rule.TrafficLimitBytes) ==
			(rule.TrafficLimitBytes > 0 && rule.TrafficUsedBytes >= rule.TrafficLimitBytes) {
		rule.TrafficUsedBytes = value.TrafficUsedBytes
	}
	return rule
}

func (s *Store) MergeRuleTrafficOverlays(overlays []RuleTrafficOverlay) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, overlay := range overlays {
		rule, ok := s.forwardRules[overlay.State.ID]
		if !ok {
			continue
		}
		merged := MergeRuleTrafficOverlay(forwarding.TrafficState(rule), overlay)
		if merged.TrafficUsedBytes != rule.TrafficUsedBytes {
			rule.TrafficUsedBytes = merged.TrafficUsedBytes
			rule.UpdatedAt = overlay.UpdatedAt
			s.forwardRules[rule.ID] = rule
		}
	}
}
