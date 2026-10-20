package memoryrepo

import (
	"context"
	"maps"

	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

// ForwardingReadView holds detached, immutable inputs, not cached activation
// results. It exposes no Store, mutation, history reader or runtime secret API.
// Every request gets private overlay maps and derives readiness at the current
// time using the ordinary repository checks.
type ForwardingReadView struct{ state *Store }

func (s *Store) ForwardingReadView() *ForwardingReadView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := &Store{
		persistenceRevision:     s.persistenceRevision,
		gatewayMutationRevision: s.gatewayMutationRevision + uint64(len(s.auditEvents)),
		businessIdempotency:     cloneViewMap(s.businessIdempotency, func(m businessMutation) businessMutation { m.Result = append([]byte(nil), m.Result...); return m }),
		adminsByUsername:        cloneViewMap(s.adminsByUsername, cloneAdministrator),
		customers:               cloneViewMap(s.customers, cloneCustomer),
		groupNetworks:           cloneViewMap(s.groupNetworks, cloneGroupNetwork),
		membersByGroup: cloneViewMap(s.membersByGroup, func(m map[string]groups.Member) map[string]groups.Member {
			return cloneViewMap(m, cloneGroupMember)
		}),
		endpointPools: cloneViewMap(s.endpointPools, cloneEndpointPool),
		nodes:         cloneViewMap(s.nodes, cloneNode),
		forwardRules:  cloneViewMap(s.forwardRules, cloneForwardingRule),
		endpointMembers: cloneViewMap(s.endpointMembers, func(m map[string]endpoints.EndpointPoolMember) map[string]endpoints.EndpointPoolMember {
			return cloneViewMap(m, cloneEndpointMember)
		}),
		protocolHealth: cloneViewMap(s.protocolHealth, func(m map[string]gatewaymembership.ProtocolObservation) map[string]gatewaymembership.ProtocolObservation {
			return maps.Clone(m)
		}),
		protocolProbes:        maps.Clone(s.protocolProbes),
		autoRealityKeys:       maps.Clone(s.autoRealityKeys),
		vlessBindings:         cloneViewMap(s.vlessBindings, cloneVLESSCredentialRecord),
		vlessRuntimeMaterials: cloneViewMap(s.vlessRuntimeMaterials, cloneVLESSRuntimeMaterial),
		nodeConfigsByNode:     hotViewGenerations(s.nodes, s.nodeConfigsByNode, cloneNodeConfig),
		applyResultsByNode: hotViewGenerations(s.nodes, s.applyResultsByNode, func(results map[string]generations.ApplyResult) map[string]generations.ApplyResult {
			return maps.Clone(results)
		}),
		applyAttemptsByNode: hotViewGenerations(s.nodes, s.applyAttemptsByNode, func(attempt generations.ApplyAttemptState) generations.ApplyAttemptState {
			attempt.SeenIDs = maps.Clone(attempt.SeenIDs)
			return attempt
		}),
	}
	state.forwardingProjection = state.buildForwardingProjectionLocked()
	return &ForwardingReadView{state: state}
}

// Readiness evaluates the desired generation and, while a deployment is in
// flight, the applied generation that still serves existing rules. Other
// historical payloads remain in the authoritative store, not in this
// frequently rebuilt projection.
func hotViewGenerations[V any](nodesByID map[string]nodes.Node, input map[string]map[int64]V, clone func(V) V) map[string]map[int64]V {
	result := make(map[string]map[int64]V, len(nodesByID))
	for id, node := range nodesByID {
		generations := make(map[int64]V, 2)
		for _, generation := range []int64{node.DesiredGeneration, node.AppliedGeneration} {
			if generation <= 0 {
				continue
			}
			if value, ok := input[id][generation]; ok {
				generations[generation] = clone(value)
			}
		}
		if len(generations) > 0 {
			result[id] = generations
		}
	}
	return result
}

func cloneViewMap[K comparable, V any](input map[K]V, clone func(V) V) map[K]V {
	result := make(map[K]V, len(input))
	for key, value := range input {
		result[key] = clone(value)
	}
	return result
}

// WithOverlays shares only private immutable inputs. The maps changed by the
// overlay merge methods belong to this request. Returned repository DTOs retain
// their ordinary defensive copies; no caller can mutate the cached inputs.
func (v *ForwardingReadView) WithOverlays(revision uint64, traffic []RuleTrafficOverlay, health []gatewaymembership.ProtocolObservation, telemetry []nodes.Node) *ForwardingReadView {
	base := v.state
	state := &Store{
		persistenceRevision: revision, gatewayMutationRevision: base.gatewayMutationRevision,
		forwardingProjection: base.forwardingProjection,
		businessIdempotency:  base.businessIdempotency,
		adminsByUsername:     base.adminsByUsername, customers: base.customers,
		groupNetworks: base.groupNetworks, membersByGroup: base.membersByGroup,
		endpointPools: base.endpointPools, protocolProbes: base.protocolProbes,
		autoRealityKeys: base.autoRealityKeys, vlessBindings: base.vlessBindings,
		vlessRuntimeMaterials: base.vlessRuntimeMaterials,
		nodeConfigsByNode:     base.nodeConfigsByNode, applyResultsByNode: base.applyResultsByNode,
		applyAttemptsByNode: base.applyAttemptsByNode,
		nodes:               maps.Clone(base.nodes), forwardRules: maps.Clone(base.forwardRules),
		endpointMembers: cloneViewMap(base.endpointMembers, func(m map[string]endpoints.EndpointPoolMember) map[string]endpoints.EndpointPoolMember {
			return maps.Clone(m)
		}),
		protocolHealth: cloneViewMap(base.protocolHealth, func(m map[string]gatewaymembership.ProtocolObservation) map[string]gatewaymembership.ProtocolObservation {
			return maps.Clone(m)
		}),
	}
	state.MergeRuleTrafficOverlays(traffic)
	state.MergeProtocolHealthOverlays(health)
	state.MergeHeartbeatTelemetry(telemetry)
	return &ForwardingReadView{state: state}
}

func (v *ForwardingReadView) ListForwardingRules(ctx context.Context) ([]forwarding.Rule, error) {
	return v.state.ListForwardingRules(ctx)
}
func (v *ForwardingReadView) ForwardingRule(ctx context.Context, id string) (forwarding.Rule, error) {
	return v.state.ForwardingRule(ctx, id)
}
func (v *ForwardingReadView) GatewayMembershipState(ctx context.Context, id string) (gatewaymembership.State, error) {
	return v.state.GatewayMembershipState(ctx, id)
}
func (v *ForwardingReadView) GatewayMembershipStates(ctx context.Context, ids []string) ([]gatewaymembership.State, error) {
	return v.state.GatewayMembershipStates(ctx, ids)
}
func (v *ForwardingReadView) ProtocolProbeStates(ctx context.Context) ([]gatewaymembership.ProbeState, error) {
	return v.state.ProtocolProbeStates(ctx)
}

func (v *ForwardingReadView) ListManagedForwardingRules(ctx context.Context, adminID string) ([]forwarding.Rule, error) {
	return v.state.ListManagedForwardingRules(ctx, adminID)
}

func (v *ForwardingReadView) ManagedForwardingRule(ctx context.Context, adminID, id string) (forwarding.Rule, error) {
	return v.state.ManagedForwardingRule(ctx, adminID, id)
}

// CheckProtocolHealthRenewal validates proofs on a detached request copy. It
// never activates a rule or changes this view. First activation must be replayed
// by the persistence adapter against its complete audited write state.
func (v *ForwardingReadView) CheckProtocolHealthRenewal(ctx context.Context, observations []gatewaymembership.ProtocolObservation) (results []error, changed, needsActivation bool, err error) {
	if err = ctx.Err(); err != nil {
		return nil, false, false, err
	}
	state := v.WithOverlays(v.state.persistenceRevision, nil, nil, nil).state
	for _, observation := range observations {
		if err = ctx.Err(); err != nil {
			return nil, false, false, err
		}
		// This flag selects the audited first-activation transaction. The
		// public projection clears Deployed when a lease expires; consulting
		// it here would turn ordinary recovery/renewal into a full snapshot
		// rewrite and evaluate every group member for each observation.
		// PublishProtocolHealth below still checks all current proof bindings.
		if rule, exists := state.forwardRules[observation.RuleID]; exists && !rule.Deployed {
			needsActivation = true
		}
	}
	results = make([]error, len(observations))
	for i, observation := range observations {
		if err = ctx.Err(); err != nil {
			return nil, false, false, err
		}
		results[i] = state.PublishProtocolHealth(ctx, observation)
		changed = changed || results[i] == nil
	}
	return results, changed, needsActivation, nil
}
