package memoryrepo

import (
	"context"
	"maps"

	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

// ForwardingReadView holds detached, immutable inputs, not cached activation
// results. It exposes no Store, mutation or history reader. Desired config and
// protocol probe reads are internal confidential APIs; public rule reads keep
// the ordinary credential redaction.
// Every request detaches the maps its overlays change and derives readiness at
// the current time using the ordinary repository checks.
type ForwardingReadView struct{ state *Store }

func (s *Store) ForwardingReadView() *ForwardingReadView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := &Store{
		persistenceRevision:     s.persistenceRevision,
		gatewayMutationRevision: s.gatewayMutationRevision + uint64(len(s.auditEvents)),
		// Derive grants under the source lock; the detached index contains no
		// mutation payloads. Read paths need these grants, not retry responses.
		forwardingProjection: s.buildForwardingProjectionLocked(),
		adminsByUsername:     cloneViewMap(s.adminsByUsername, cloneAdministrator),
		customers:            cloneViewMap(s.customers, cloneCustomer),
		groupNetworks:        cloneViewMap(s.groupNetworks, cloneGroupNetwork),
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
	var configs []generations.NodeConfigGeneration
	for _, byGeneration := range state.nodeConfigsByNode {
		for _, config := range byGeneration {
			configs = append(configs, config)
		}
	}
	state.receiptEvaluator = deploymentreceipts.NewEvaluator(configs)
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
			// Desired and applied normally coincide. Copy their confidential
			// payload and evidence once, retaining both when a rollout is pending.
			if _, copied := generations[generation]; copied {
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
	state := v.sharedOverlayState(revision)
	if len(traffic) > 0 {
		state.forwardRules = maps.Clone(state.forwardRules)
		state.MergeRuleTrafficOverlays(traffic)
	}
	if len(health) > 0 {
		var privatePools map[string]bool
		// This private shell has not escaped. Detach only after a committed
		// observation passes restoration ordering, immediately before writing.
		state.mergeProtocolHealthOverlaysLocked(health, func(poolID string) {
			if privatePools == nil {
				state.endpointMembers = maps.Clone(state.endpointMembers)
				state.protocolHealth = maps.Clone(state.protocolHealth)
				if state.protocolHealth == nil {
					state.protocolHealth = make(map[string]map[string]gatewaymembership.ProtocolObservation)
				}
				privatePools = make(map[string]bool)
			}
			if !privatePools[poolID] {
				state.endpointMembers[poolID] = maps.Clone(state.endpointMembers[poolID])
				state.protocolHealth[poolID] = maps.Clone(state.protocolHealth[poolID])
				privatePools[poolID] = true
			}
		})
	}
	if len(telemetry) > 0 {
		state.nodes = maps.Clone(state.nodes)
		state.MergeHeartbeatTelemetry(telemetry)
	}
	return &ForwardingReadView{state: state}
}

// The returned shell borrows immutable inputs. Each caller must detach every
// map it writes before invoking a mutation; it must never escape as a Store.
func (v *ForwardingReadView) sharedOverlayState(revision uint64) *Store {
	base := v.state
	return &Store{
		persistenceRevision: revision, gatewayMutationRevision: base.gatewayMutationRevision,
		forwardingProjection: base.forwardingProjection,
		receiptEvaluator:     base.receiptEvaluator,
		adminsByUsername:     base.adminsByUsername, customers: base.customers,
		groupNetworks: base.groupNetworks, membersByGroup: base.membersByGroup,
		endpointPools: base.endpointPools, protocolProbes: base.protocolProbes,
		autoRealityKeys: base.autoRealityKeys, vlessBindings: base.vlessBindings,
		vlessRuntimeMaterials: base.vlessRuntimeMaterials,
		nodeConfigsByNode:     base.nodeConfigsByNode, applyResultsByNode: base.applyResultsByNode,
		applyAttemptsByNode: base.applyAttemptsByNode,
		nodes:               base.nodes, forwardRules: base.forwardRules,
		endpointMembers: base.endpointMembers, protocolHealth: base.protocolHealth,
	}
}

func (v *ForwardingReadView) ListForwardingRules(ctx context.Context) ([]forwarding.Rule, error) {
	return v.state.ListForwardingRules(ctx)
}
func (v *ForwardingReadView) ForwardingRule(ctx context.Context, id string) (forwarding.Rule, error) {
	return v.state.ForwardingRule(ctx, id)
}
func (v *ForwardingReadView) DesiredNodeConfig(ctx context.Context, id string) (generations.NodeConfigGeneration, int64, error) {
	return v.state.DesiredNodeConfig(ctx, id)
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

func (v *ForwardingReadView) ProtocolProbeConfig(ctx context.Context, id string) (gatewaymembership.ProtocolProbeConfig, error) {
	return v.state.ProtocolProbeConfig(ctx, id)
}

func (v *ForwardingReadView) RuleNodeDeploymentInput(ctx context.Context, ruleID, nodeID string) (deploymentreceipts.Input, error) {
	return v.state.RuleNodeDeploymentInput(ctx, ruleID, nodeID)
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
	// Publication changes only these two maps and the local mutation revision.
	// Detach the outer maps and each pool only after a proof passes
	// the ordinary validation. Nodes, rules and unrelated pools stay read-only.
	state := v.sharedOverlayState(v.state.persistenceRevision)
	var privatePools map[string]bool
	for _, observation := range observations {
		if err = ctx.Err(); err != nil {
			return nil, false, false, err
		}
		// This flag selects the audited first-activation transaction. The
		// public projection clears Deployed when a lease expires; consulting
		// it here would turn ordinary recovery/renewal into a full snapshot
		// rewrite and evaluate every group member for each observation.
		// Shared validation below still checks all current proof bindings.
		if rule, exists := state.forwardRules[observation.RuleID]; exists && !rule.Deployed {
			needsActivation = true
		}
	}
	results = make([]error, len(observations))
	for i, observation := range observations {
		if err = ctx.Err(); err != nil {
			return nil, false, false, err
		}
		poolID, member, proofErr := state.validateProtocolHealthLocked(observation)
		results[i] = proofErr
		if proofErr == nil {
			if privatePools == nil {
				state.endpointMembers = maps.Clone(state.endpointMembers)
				state.protocolHealth = maps.Clone(state.protocolHealth)
				privatePools = make(map[string]bool)
			}
			if !privatePools[poolID] {
				state.endpointMembers[poolID] = maps.Clone(state.endpointMembers[poolID])
				state.protocolHealth[poolID] = maps.Clone(state.protocolHealth[poolID])
				privatePools[poolID] = true
			}
			state.recordProtocolHealthLocked(observation, poolID, member)
		}
		changed = changed || results[i] == nil
	}
	return results, changed, needsActivation, nil
}
