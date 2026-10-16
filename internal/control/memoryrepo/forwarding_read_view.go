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
		nodeConfigsByNode:     desiredViewGenerations(s.nodes, s.nodeConfigsByNode, cloneNodeConfig),
		applyResultsByNode: desiredViewGenerations(s.nodes, s.applyResultsByNode, func(results map[string]generations.ApplyResult) map[string]generations.ApplyResult {
			return maps.Clone(results)
		}),
		applyAttemptsByNode: desiredViewGenerations(s.nodes, s.applyAttemptsByNode, func(attempt generations.ApplyAttemptState) generations.ApplyAttemptState {
			attempt.SeenIDs = maps.Clone(attempt.SeenIDs)
			return attempt
		}),
	}
	return &ForwardingReadView{state: state}
}

// Readiness only evaluates each node's desired generation. Historical payloads
// remain in the authoritative store, not in this frequently rebuilt projection.
func desiredViewGenerations[V any](nodesByID map[string]nodes.Node, input map[string]map[int64]V, clone func(V) V) map[string]map[int64]V {
	result := make(map[string]map[int64]V, len(nodesByID))
	for id, node := range nodesByID {
		if value, ok := input[id][node.DesiredGeneration]; ok && node.DesiredGeneration > 0 {
			result[id] = map[int64]V{node.DesiredGeneration: clone(value)}
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
		adminsByUsername: base.adminsByUsername, customers: base.customers,
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
