package memoryrepo

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

func (s *Store) GatewayMembershipState(_ context.Context, poolID string) (gatewaymembership.State, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.gatewayMembershipStateLocked(poolID)
}

func (s *Store) GatewayMembershipStates(ctx context.Context, poolIDs []string) ([]gatewaymembership.State, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	states := make([]gatewaymembership.State, 0, len(poolIDs))
	for _, poolID := range poolIDs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		state, err := s.gatewayMembershipStateLocked(poolID)
		if err == faults.ErrNotFound {
			// A pool may be deleted between metadata and readiness reads.
			// Its missing evidence must never make it selectable.
			continue
		}
		if err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	return states, nil
}

func (s *Store) gatewayMembershipStateLocked(poolID string) (gatewaymembership.State, error) {
	pool, found := s.endpointPools[poolID]
	if !found {
		return gatewaymembership.State{}, faults.ErrNotFound
	}
	state := gatewaymembership.State{
		Revision: uint64(len(s.auditEvents)) + s.gatewayMutationRevision + 1, Pool: cloneEndpointPool(pool),
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
			// The gateway derives readiness from evidence below, independently
			// from the public rule projection, to avoid a circular health check.
			state.Rule = s.forwardingBaseViewLocked(rule)
		}
	}
	state.Members = make([]endpoints.EndpointPoolMember, 0, len(s.endpointMembers[poolID]))
	for _, member := range s.endpointMembers[poolID] {
		state.Members = append(state.Members, cloneEndpointMember(member))
		if state.Rule.ID == "" {
			continue
		}
		input, err := s.ruleNodeDeploymentInputBorrowedLocked(state.Rule.ID, member.NodeID)
		if err == nil {
			state.Deployments[member.NodeID] = gatewaymembership.DeploymentEvidence{
				RuleRevision: input.Rule.Revision, Status: s.evaluateDeploymentLocked(input),
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

// PersistenceMutationState identifies the parts of a protocol-health batch
// that require a snapshot write. The health projection advances the gateway
// mutation revision, while first activation appends an audit event.
// Callers use the pair to distinguish a rejected/no-op probe batch from a
// batch that must be persisted.
func (s *Store) PersistenceMutationState() (gatewayRevision uint64, auditCount int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.gatewayMutationRevision, len(s.auditEvents)
}

// PublishProtocolHealth stores a rule/config-bound observation produced by
// ProtocolObserver and refreshes the endpoint member's protocol-backed health
// lease. This is deliberately separate from node heartbeat state: only a
// successful VLESS protocol challenge may refresh LastHealthAt.
func (s *Store) PublishProtocolHealth(_ context.Context, observation gatewaymembership.ProtocolObservation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.publishProtocolHealthLocked(observation)
}

// endpointPoolIDForRuleLocked resolves the stable endpoint owned by a rule.
// A rule is intentionally bound to one pool. Older snapshots and migration
// imports can violate that invariant, so never pick a pool by map iteration:
// doing so makes protocol health and activation depend on random map order.
func (s *Store) endpointPoolIDForRuleLocked(ruleID string) (string, error) {
	if ruleID == "" {
		return "", faults.ErrValidation
	}
	poolID := ""
	for id, pool := range s.endpointPools {
		if pool.RuleID != ruleID {
			continue
		}
		if poolID != "" {
			return "", fmt.Errorf("%w: forwarding rule %q has multiple endpoint pools", faults.ErrConflict, ruleID)
		}
		poolID = id
	}
	if poolID == "" {
		return "", faults.ErrNotFound
	}
	return poolID, nil
}

func (s *Store) publishProtocolHealthLocked(observation gatewaymembership.ProtocolObservation) error {
	if observation.RuleID == "" || observation.NodeID == "" || observation.RuleRevision < 1 ||
		observation.NodeConfigGeneration < 1 || len(observation.ConfigSHA256) != 64 ||
		observation.VerifiedAt.IsZero() || !observation.LeaseExpiresAt.After(observation.VerifiedAt) ||
		observation.LeaseExpiresAt.After(observation.VerifiedAt.Add(endpoints.DefaultHealthTTL)) {
		return faults.ErrValidation
	}
	poolID, err := s.endpointPoolIDForRuleLocked(observation.RuleID)
	if err != nil {
		return err
	}
	members := s.endpointMembers[poolID]
	member, exists := members[observation.NodeID]
	if !exists || member.PoolID != poolID || member.GroupID == "" || member.State != endpoints.CandidateEligible || member.Weight < 1 {
		return faults.ErrNotFound
	}
	if member.DialHost == "" {
		return faults.ErrValidation
	}
	input, err := s.ruleNodeDeploymentInputBorrowedLocked(observation.RuleID, observation.NodeID)
	if err != nil {
		return err
	}
	evidence := gatewaymembership.DeploymentEvidence{RuleRevision: input.Rule.Revision, Status: s.evaluateDeploymentLocked(input)}
	if !observation.MatchesCurrentDeployment(s.endpointPools[poolID], input.Rule, member, evidence, time.Now().UTC()) {
		return faults.ErrValidation
	}
	if previous, exists := s.protocolHealth[poolID][observation.NodeID]; exists && !observation.CanReplace(previous) {
		return faults.ErrConflict
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
	s.gatewayMutationRevision++
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
	s.gatewayMutationRevision++
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

// ProtocolProbeStates decodes the control-plane snapshot once per scheduling
// refresh, rather than once per rule and again for each node challenge.
func (s *Store) ProtocolProbeStates(ctx context.Context) ([]gatewaymembership.ProbeState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]gatewaymembership.ProbeState, 0, len(s.endpointPools))
	seenRules := make(map[string]string)
	for id, pool := range s.endpointPools {
		if pool.Protocol != "vless" || pool.RuleID == "" {
			continue
		}
		if previous, exists := seenRules[pool.RuleID]; exists {
			return nil, fmt.Errorf("%w: forwarding rule %q has endpoint pools %q and %q", faults.ErrConflict, pool.RuleID, previous, id)
		}
		seenRules[pool.RuleID] = id
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		state, err := s.gatewayMembershipStateLocked(id)
		if err != nil {
			return nil, err
		}
		items = append(items, gatewaymembership.ProbeState{State: state, Config: s.protocolProbes[pool.RuleID]})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].State.Pool.ID < items[j].State.Pool.ID })
	return items, nil
}

// PublishProtocolHealthBatch validates each observation independently against
// current state. A stale probe cannot discard unrelated fresh health results.
// Activation happens once per newly ready rule in this same transaction.
func (s *Store) PublishProtocolHealthBatch(ctx context.Context, observations []gatewaymembership.ProtocolObservation) ([]error, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	errors := make([]error, len(observations))
	ready := make(map[string]int64)
	for i, observation := range observations {
		errors[i] = s.publishProtocolHealthLocked(observation)
		if errors[i] == nil {
			ready[observation.RuleID] = observation.RuleRevision
		}
	}
	for id, revision := range ready {
		alreadyActive := s.forwardRules[id].Deployed
		if alreadyActive {
			continue
		}
		event, err := audit.NewEvent(time.Now().UTC(), "system", "protocol-runner", "forwarding_rule.activate", "forwarding_rule", id, "succeeded", nil)
		if err == nil {
			err = s.markActivatedLocked(id, revision, event)
		}
		if err != nil {
			for i, observation := range observations {
				if observation.RuleID == id && errors[i] == nil {
					errors[i] = err
				}
			}
		}
	}
	return errors, nil
}
