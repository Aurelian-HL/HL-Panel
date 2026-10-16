package memoryrepo

import (
	"encoding/json"
	"math"
	"strconv"
	"time"

	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/forwarding"
)

const ruleConnectionFreshness = 5 * time.Second

func (s *Store) ruleConnectionLocked(rule forwarding.Rule, now time.Time) *forwarding.RuleConnection {
	result := &forwarding.RuleConnection{Status: "offline", Reason: "not_sampled"}
	if rule.Paused || (rule.Status != forwarding.StatusActive && rule.Status != forwarding.StatusPendingActivation) {
		result.Reason = "inactive"
		return result
	}
	if rule.Protocol != forwarding.ProtocolTCP {
		result.Reason = "tcp_only"
		return result
	}
	for nodeID, member := range s.membersByGroup[rule.EntryGroupID] {
		node, exists := s.nodes[nodeID]
		if !exists || member.RetiredAt != nil || node.DeletedAt != nil || node.LastHeartbeatAt == nil {
			continue
		}
		age := now.Sub(*node.LastHeartbeatAt)
		if age < 0 || age > ruleConnectionFreshness {
			continue
		}
		if node.LastApplyGeneration == node.DesiredGeneration && node.LastApplyStatus == "failed" {
			result.ErrorNodes++
			continue
		}
		// A matching real-engine receipt prevents another process using the same
		// local port from making a pending/unapplied rule appear online.
		input, err := s.ruleNodeDeploymentInputBorrowedLocked(rule.ID, nodeID)
		if err != nil || !deploymentreceipts.Evaluate(input).ReceiptVerified {
			continue
		}
		host, ok := node.Resources["host"].(map[string]any)
		if !ok {
			continue
		}
		count, listening, sampled := listenerConnectionCount(host["tcp_listeners"], strconv.Itoa(rule.ListenPort))
		if !sampled {
			continue
		}
		if result.SampledAt == nil || node.LastHeartbeatAt.Before(*result.SampledAt) {
			at := *node.LastHeartbeatAt
			result.SampledAt = &at
		}
		if !listening {
			result.ErrorNodes++
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
	case result.ErrorNodes > 0:
		result.Status, result.Reason = "error", "listener_or_apply_failed"
	case result.SampledAt != nil:
		result.Reason = "no_connections"
	}
	return result
}

func listenerConnectionCount(value any, port string) (count uint64, listening, sampled bool) {
	switch listeners := value.(type) {
	case map[string]uint64:
		if listeners == nil {
			return
		}
		count, listening = listeners[port]
		return count, listening, count <= 1<<53-1
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
