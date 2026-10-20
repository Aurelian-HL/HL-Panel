package memoryrepo

import (
	"fmt"
	"maps"
	"time"

	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
)

// A bound endpoint follows its rule's port, while retaining the same pool,
// identities and device group. Moving groups or changing ingress still requires
// a separate workflow; it cannot silently repurpose an issued identity.
func (s *Store) validateBoundEndpointUpdateLocked(rule forwarding.Rule) error {
	var bound endpoints.EndpointPool
	for _, pool := range s.endpointPools {
		if pool.RuleID != rule.ID {
			continue
		}
		if bound.ID != "" {
			return fmt.Errorf("%w: forwarding rule has multiple bound service endpoints", faults.ErrConflict)
		}
		protocolMatches := pool.Protocol == "vless" && rule.EffectiveIngressProtocol() == forwarding.IngressVLESSReality ||
			pool.Protocol == "tcp" && rule.EffectiveIngressProtocol() == forwarding.IngressTCP ||
			pool.Protocol == "socks5" && rule.EffectiveIngressProtocol() == forwarding.IngressSOCKS5
		if pool.GroupID != rule.EntryGroupID || !protocolMatches {
			return fmt.Errorf("%w: forwarding rule change would invalidate a bound service endpoint", faults.ErrConflict)
		}
		bound = pool
	}
	if bound.ID == "" || bound.Port == rule.ListenPort {
		return nil
	}
	for _, pool := range s.endpointPools {
		if pool.ID != bound.ID && pool.Protocol == bound.Protocol && pool.Hostname == bound.Hostname && pool.Port == rule.ListenPort {
			return fmt.Errorf("%w: stable endpoint is already allocated", faults.ErrConflict)
		}
	}
	return nil
}

// Called with the store write lock after validation and before compilation.
// PostgreSQL persists this projection in the same transaction as the rule.
func (s *Store) syncBoundEndpointPortLocked(rule forwarding.Rule, changedAt time.Time) func() {
	for poolID, pool := range s.endpointPools {
		if pool.RuleID != rule.ID || pool.Port == rule.ListenPort {
			continue
		}
		previousPool := pool
		previousMembers := s.endpointMembers[poolID]
		previousHealth, hadHealth := s.protocolHealth[poolID]
		pool.Port = rule.ListenPort
		pool.HealthyCandidateCount = 0
		pool.UpdatedAt = changedAt
		s.endpointPools[poolID] = pool
		members := maps.Clone(previousMembers)
		for nodeID, member := range members {
			member.LastHealthAt = nil
			member.LastHealthReason = "listener_port_changed"
			member.UpdatedAt = changedAt
			members[nodeID] = member
		}
		s.endpointMembers[poolID] = members
		delete(s.protocolHealth, poolID)
		return func() {
			s.endpointPools[poolID] = previousPool
			s.endpointMembers[poolID] = previousMembers
			if hadHealth {
				s.protocolHealth[poolID] = previousHealth
			} else {
				delete(s.protocolHealth, poolID)
			}
		}
	}
	return nil
}
