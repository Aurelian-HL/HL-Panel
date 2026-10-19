package memoryrepo

import (
	"encoding/json"
	"math"
	"strconv"
	"time"

	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/forwarding"
)

const (
	ruleConnectionHeartbeatFreshness = 5 * time.Second
	ruleConnectionFreshness          = 5*time.Minute + 30*time.Second
)

func (s *Store) ruleConnectionLocked(rule forwarding.Rule, now time.Time) *forwarding.RuleConnection {
	result := &forwarding.RuleConnection{Status: "offline", Reason: "not_sampled"}
	if rule.Paused || (rule.Status != forwarding.StatusActive && rule.Status != forwarding.StatusPendingActivation) {
		result.Reason = "inactive"
		return result
	}
	// Connection telemetry is a TCP-listener observation. The target protocol
	// is a separate concern: VLESS Reality and SOCKS5 both use TCP ingress,
	// while a legacy/malformed record must still be classified by its explicit
	// customer-facing ingress when available.
	if rule.EffectiveIngressProtocol() == forwarding.IngressUDP {
		result.Reason = "tcp_only"
		return result
	}
	observe := func(at time.Time, lease time.Duration) {
		if result.SampledAt == nil || at.Before(*result.SampledAt) {
			result.SampledAt = &at
		}
		if result.ValidForMS == 0 || lease.Milliseconds() < result.ValidForMS {
			result.ValidForMS = max(0, lease.Milliseconds())
		}
	}
	for nodeID, member := range s.membersByGroup[rule.EntryGroupID] {
		node, exists := s.nodes[nodeID]
		if !exists || member.RetiredAt != nil || node.DeletedAt != nil {
			continue
		}
		if node.LastHeartbeatAt == nil {
			result.StaleNodes++
			continue
		}
		age := now.Sub(*node.LastHeartbeatAt)
		host, ok := node.Resources["host"].(map[string]any)
		if !ok {
			result.StaleNodes++
			continue
		}
		heartbeatFreshness := ruleConnectionHeartbeatFreshness
		if _, exists := host["tcp_listeners_sample_age_ms"]; exists {
			heartbeatFreshness = ruleConnectionFreshness
		}
		if age < 0 || age >= heartbeatFreshness {
			result.StaleNodes++
			continue
		}
		sampledAt := *node.LastHeartbeatAt
		sampleFreshness := ruleConnectionHeartbeatFreshness
		if raw, exists := host["tcp_listeners_sample_age_ms"]; exists {
			sampleAge, ok := tcpSampleAge(raw)
			if !ok {
				result.StaleNodes++
				continue
			}
			sampledAt = sampledAt.Add(-sampleAge)
			sampleFreshness = ruleConnectionFreshness
		}
		sampleAge := now.Sub(sampledAt)
		if sampleAge < 0 || sampleAge >= sampleFreshness {
			result.StaleNodes++
			continue
		}
		lease := min(heartbeatFreshness-age, sampleFreshness-sampleAge)
		applyFailed := node.LastApplyGeneration == node.DesiredGeneration && node.LastApplyStatus == "failed"
		if applyFailed {
			// A failed full-bundle update does not prove that this existing
			// listener stopped. Keep the update diagnostic separate from the
			// rule-scoped socket evidence; never erase a known-good count.
			result.ApplyFailedNodes++
			result.ErrorNodes++
			observe(sampledAt, lease)
		}
		// A matching real-engine receipt prevents another process using the same
		// local port from making a pending/unapplied rule appear online.
		input, err := s.ruleNodeDeploymentInputBorrowedLocked(rule.ID, nodeID)
		if err != nil || !deploymentreceipts.Evaluate(input).ReceiptVerified {
			result.PendingNodes++
			continue
		}
		count, listening, sampled := listenerConnectionCount(host["tcp_listeners"], strconv.Itoa(rule.ListenPort))
		if !sampled {
			continue
		}
		observe(sampledAt, lease)
		result.SampledNodes++
		if !listening {
			result.MissingListenerNodes++
			if !applyFailed {
				result.ErrorNodes++
			}
			continue
		}
		// JSON numbers must remain exact; an unrepresentable sample is unknown.
		if result.Connections <= (1<<53-1)-count {
			result.Connections += count
		}
	}
	switch {
	case result.Connections > 0:
		result.Status, result.Reason = "online", "established"
	case result.MissingListenerNodes > 0:
		result.Status, result.Reason = "error", "listener_missing"
	case result.ApplyFailedNodes > 0:
		result.Status, result.Reason = "error", "node_apply_failed"
	case result.SampledNodes > 0:
		result.Reason = "no_connections"
	case result.PendingNodes > 0:
		result.Reason = "deployment_pending"
	case result.StaleNodes > 0:
		result.Reason = "sample_expired"
	}
	return result
}

func tcpSampleAge(value any) (time.Duration, bool) {
	var ms float64
	switch v := value.(type) {
	case float64:
		ms = v
	case int64:
		ms = float64(v)
	case json.Number:
		var err error
		ms, err = v.Float64()
		if err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	if math.IsNaN(ms) || math.IsInf(ms, 0) || ms < 0 || ms > float64(ruleConnectionFreshness.Milliseconds()) || math.Trunc(ms) != ms {
		return 0, false
	}
	return time.Duration(ms) * time.Millisecond, true
}

func listenerConnectionCount(value any, port string) (count uint64, listening, sampled bool) {
	switch listeners := value.(type) {
	case map[string]uint64:
		if listeners == nil {
			return
		}
		var exists bool
		count, exists = listeners[port]
		return count, exists, count <= 1<<53-1
	case map[string]any:
		if listeners == nil {
			return
		}
		raw, exists := listeners[port]
		if !exists {
			return 0, false, true
		}
		var n float64
		switch v := raw.(type) {
		case float64:
			n = v
		case uint64:
			if v <= 1<<53-1 {
				return v, true, true
			}
			return
		case json.Number:
			var err error
			n, err = v.Float64()
			if err != nil {
				return
			}
		default:
			return
		}
		if !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 && n <= 1<<53-1 && math.Trunc(n) == n {
			return uint64(n), true, true
		}
	}
	return
}
