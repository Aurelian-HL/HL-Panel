package memoryrepo

import (
	"context"
	"maps"
	"sort"
	"time"

	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

func (s *Store) GatewayMembershipState(_ context.Context, poolID string) (gatewaymembership.State, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pool, found := s.endpointPools[poolID]
	if !found {
		return gatewaymembership.State{}, faults.ErrNotFound
	}
	state := gatewaymembership.State{
		Revision: uint64(len(s.auditEvents)) + 1, Pool: cloneEndpointPool(pool),
		Deployments:    make(map[string]gatewaymembership.DeploymentEvidence),
		ProtocolHealth: make(map[string]gatewaymembership.ProtocolObservation),
	}
	if s.persistenceRevision != 0 {
		state.Revision = s.persistenceRevision
	}
	for nodeID, observation := range s.protocolHealth[poolID] {
		state.ProtocolHealth[nodeID] = observation
	}
	if pool.RuleID != "" {
		if rule, exists := s.forwardRules[pool.RuleID]; exists {
			state.Rule = s.forwardingViewLocked(rule)
		}
	}
	state.Members = make([]endpoints.EndpointPoolMember, 0, len(s.endpointMembers[poolID]))
	for _, member := range s.endpointMembers[poolID] {
		state.Members = append(state.Members, cloneEndpointMember(member))
		if state.Rule.ID == "" {
			continue
		}
		input, err := s.ruleNodeDeploymentInputLocked(state.Rule.ID, member.NodeID)
		if err == nil {
			state.Deployments[member.NodeID] = gatewaymembership.DeploymentEvidence{
				RuleRevision: input.Rule.Revision, Status: deploymentreceipts.Evaluate(input),
			}
		}
	}
	sort.Slice(state.Members, func(i, j int) bool { return state.Members[i].NodeID < state.Members[j].NodeID })
	return state, nil
}

// SetPersistenceRevision binds a decoded read projection to the database
// revision, independent of the separately persisted audit history.
func (s *Store) SetPersistenceRevision(revision uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.persistenceRevision = revision
}

// PublishProtocolHealth stores a rule/config-bound observation produced by
// ProtocolObserver and refreshes the endpoint member's protocol-backed health
// lease. This is deliberately separate from node heartbeat state: only a
// successful VLESS protocol challenge may refresh LastHealthAt.
func (s *Store) PublishProtocolHealth(_ context.Context, observation gatewaymembership.ProtocolObservation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if observation.RuleID == "" || observation.NodeID == "" || observation.RuleRevision < 1 ||
		observation.NodeConfigGeneration < 1 || len(observation.ConfigSHA256) != 64 ||
		observation.VerifiedAt.IsZero() || !observation.LeaseExpiresAt.After(observation.VerifiedAt) ||
		observation.LeaseExpiresAt.After(observation.VerifiedAt.Add(endpoints.DefaultHealthTTL)) {
		return faults.ErrValidation
	}
	poolID := ""
	for id, pool := range s.endpointPools {
		if pool.RuleID == observation.RuleID {
			poolID = id
			break
		}
	}
	if poolID == "" {
		return faults.ErrNotFound
	}
	members := s.endpointMembers[poolID]
	member, exists := members[observation.NodeID]
	if !exists || member.PoolID != poolID || member.GroupID == "" || member.State != endpoints.CandidateEligible || member.Weight < 1 {
		return faults.ErrNotFound
	}
	if member.DialHost == "" {
		return faults.ErrValidation
	}
	if s.protocolHealth[poolID] == nil {
		s.protocolHealth[poolID] = make(map[string]gatewaymembership.ProtocolObservation)
	}
	s.protocolHealth[poolID][observation.NodeID] = observation
	verifiedAt := observation.VerifiedAt.UTC()
	member.LastHealthAt = &verifiedAt
	member.LastHealthReason = "protocol_probe"
	member.UpdatedAt = verifiedAt
	members[observation.NodeID] = member
	return nil
}

func (s *Store) ConfigureProtocolProbe(_ context.Context, ruleID string, config gatewaymembership.ProtocolProbeConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ruleID == "" || config.UUID == "" || config.EchoPort < 1 || config.EchoPort > 65535 {
		return faults.ErrValidation
	}
	if _, exists := s.forwardRules[ruleID]; !exists {
		return faults.ErrNotFound
	}
	for _, binding := range s.vlessBindings {
		if binding.Binding.ForwardingRuleID == ruleID && binding.CredentialUUID == config.UUID {
			return faults.ErrConflict
		}
	}
	for _, existing := range s.protocolProbes {
		if existing.UUID == config.UUID && existing.EchoPort == config.EchoPort {
			// A probe identity is allowed to serve exactly one rule. This keeps
			// challenge results unambiguous across configuration generations.
			return faults.ErrConflict
		}
	}
	rule, exists := s.forwardRules[ruleID]
	if !exists {
		return faults.ErrNotFound
	}
	previous, hadPrevious := s.protocolProbes[ruleID]
	previousNodes := make(map[string]nodes.Node)
	previousConfigs := make(map[string]map[int64]generations.NodeConfigGeneration)
	for nodeID, member := range s.membersByGroup[rule.EntryGroupID] {
		if member.RetiredAt != nil {
			continue
		}
		previousNodes[nodeID] = s.nodes[nodeID]
		if configs := s.nodeConfigsByNode[nodeID]; configs != nil {
			previousConfigs[nodeID] = maps.Clone(configs)
		}
	}
	s.protocolProbes[ruleID] = config
	if err := s.recompileForwardingGroupsLocked([]string{rule.EntryGroupID}, time.Now().UTC()); err != nil {
		if hadPrevious {
			s.protocolProbes[ruleID] = previous
		} else {
			delete(s.protocolProbes, ruleID)
		}
		for nodeID, previousNode := range previousNodes {
			s.nodes[nodeID] = previousNode
			if configs := previousConfigs[nodeID]; configs != nil {
				s.nodeConfigsByNode[nodeID] = configs
			} else {
				delete(s.nodeConfigsByNode, nodeID)
			}
		}
		return err
	}
	return nil
}

func (s *Store) ProtocolProbeConfig(_ context.Context, ruleID string) (gatewaymembership.ProtocolProbeConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	config, exists := s.protocolProbes[ruleID]
	if !exists {
		return gatewaymembership.ProtocolProbeConfig{}, faults.ErrNotFound
	}
	return config, nil
}
