package memoryrepo

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"sort"
	"strings"
	"time"

	agentconfig "github.com/hongle/hl-panel/internal/agent/config"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/forwarding/gostconfig"
	"github.com/hongle/hl-panel/internal/control/forwarding/vlessconfig"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/importedconfig"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/idgen"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// NodeBundleCompilerRevision advances only when persisted engine fragments
// require reconciliation after a compiler change. It is independent of panel
// releases so routine upgrades do not recompile every node's configuration.
// Revision 1 gives the dedicated protocol probe an exact loopback permission
// required by Xray 26.10.10 and later.
// Revision 2 explicitly allows third-party Reality client version numbering.
// Revision 3 drops unsupported listener metadata from rejected legacy bundles.
const NodeBundleCompilerRevision = 3

func (s *Store) compileNodeConfigLocked(nodeID string, now time.Time) (generations.NodeConfigGeneration, bool, error) {
	// A tombstone preserves history but must never receive fresh assignments.
	if node, exists := s.nodes[nodeID]; !exists || node.DeletedAt != nil {
		return generations.NodeConfigGeneration{}, false, nil
	}
	fragments := make([]agentv1.ConfigurationFragment, 0)
	for groupID, members := range s.membersByGroup {
		member, exists := members[nodeID]
		if !exists || member.RetiredAt != nil {
			continue
		}
		revisions := s.revisionsByGroup[groupID]
		if len(revisions) == 0 {
			continue
		}
		latest := revisions[len(revisions)-1]
		fragments = append(fragments, agentv1.ConfigurationFragment{
			GroupID:       groupID,
			GroupRevision: agentv1.GroupRevision(latest.Revision),
			Engine:        latest.Engine,
			Config:        cloneRaw(latest.Config),
		})
	}
	forwardingFragments, err := s.compileForwardingFragmentsLocked(nodeID)
	if err != nil {
		return generations.NodeConfigGeneration{}, false, err
	}
	// Retain metadata already accepted by legacy agents without enabling a new
	// traffic-control capability. New legacy bundles keep their original shape.
	listenerMetadata := nodeSupportsTCSpeedLimit(s.nodes[nodeID])
	if !listenerMetadata {
		node := s.nodes[nodeID]
		var previous agentv1.ConfigurationBundle
		if current, ok := s.nodeConfigsByNode[nodeID][node.AppliedGeneration]; ok && node.AppliedGeneration > 0 && json.Unmarshal(current.Config, &previous) == nil {
			for _, fragment := range previous.Fragments {
				if fragment.Usage != nil && fragment.Usage.ListenPort > 0 {
					listenerMetadata = true
					break
				}
			}
		}
	}
	if nodeSupportsUsageGeneration(s.nodes[nodeID]) {
		for i := range forwardingFragments {
			ruleID := strings.TrimPrefix(strings.TrimPrefix(forwardingFragments[i].GroupID, generations.ForwardingVLESSFragmentPrefix), generations.ForwardingGOSTFragmentPrefix)
			rule, exists := s.forwardRules[ruleID]
			if !exists {
				return generations.NodeConfigGeneration{}, false, fmt.Errorf("compiled usage rule is missing")
			}
			entry := int64(agentv1.UsageMultiplierScale)
			exit := int64(agentv1.UsageMultiplierScale)
			if n, ok := s.groupNetworks[rule.EntryGroupID]; ok {
				entry = int64(math.Round(n.TrafficMultiplier * float64(agentv1.UsageMultiplierScale)))
			}
			if rule.EgressMode == forwarding.EgressExitGroup {
				if n, ok := s.groupNetworks[rule.ExitGroupID]; ok {
					exit = int64(math.Round(n.TrafficMultiplier * float64(agentv1.UsageMultiplierScale)))
				}
			}
			speedLimit := rule.SpeedLimitMbps
			if rule.EffectiveIngressProtocol() == forwarding.IngressVLESSReality {
				speedLimit = effectiveVLESSSpeedLimit(s, rule)
			}
			// Preserve the negotiated or previously accepted metadata shape.
			listenPort := 0
			if listenerMetadata {
				listenPort = rule.ListenPort
			}
			forwardingFragments[i].Usage = &agentv1.UsageMetadata{RuleID: rule.ID, CustomerID: rule.CustomerID,
				EntryGroupID: rule.EntryGroupID, ExitGroupID: rule.ExitGroupID, Protocol: string(rule.Protocol),
				ListenPort: listenPort, SpeedLimitMbps: speedLimit,
				EntryMultiplierMicros: entry, ExitMultiplierMicros: exit}
		}
	}
	fragments = append(fragments, forwardingFragments...)
	if len(fragments) == 0 && s.nodes[nodeID].DesiredGeneration == 0 {
		node := s.nodes[nodeID]
		node.BundleCompilerRevision = NodeBundleCompilerRevision
		s.nodes[nodeID] = node
		return generations.NodeConfigGeneration{}, false, nil
	}
	sort.Slice(fragments, func(left, right int) bool { return fragments[left].GroupID < fragments[right].GroupID })
	rawBundle, err := json.Marshal(agentv1.ConfigurationBundle{SchemaVersion: 1, Fragments: fragments})
	if err != nil {
		return generations.NodeConfigGeneration{}, false, fmt.Errorf("compile node bundle: %w", err)
	}
	canonicalBundle, err := generations.CanonicalJSON(rawBundle)
	if err != nil {
		return generations.NodeConfigGeneration{}, false, err
	}
	configHash := generations.SHA256Hex(canonicalBundle)
	node := s.nodes[nodeID]
	if node.DesiredGeneration > 0 {
		current := s.nodeConfigsByNode[nodeID][node.DesiredGeneration]
		if current.ConfigSHA256 == configHash {
			node.BundleCompilerRevision = NodeBundleCompilerRevision
			s.nodes[nodeID] = node
			return cloneNodeConfig(current), false, nil
		}
	}
	id, err := idgen.New("ncfg")
	if err != nil {
		return generations.NodeConfigGeneration{}, false, err
	}
	node.DesiredGeneration++
	node.BundleCompilerRevision = NodeBundleCompilerRevision
	node.UpdatedAt = now
	s.nodes[nodeID] = node
	configuration := generations.NodeConfigGeneration{
		ID:           id,
		NodeID:       nodeID,
		Generation:   node.DesiredGeneration,
		Engine:       agentv1.EngineNodeBundle,
		Config:       canonicalBundle,
		ConfigSHA256: configHash,
		CreatedAt:    now,
	}
	if _, exists := s.nodeConfigsByNode[nodeID]; !exists {
		s.nodeConfigsByNode[nodeID] = make(map[int64]generations.NodeConfigGeneration)
	}
	s.nodeConfigsByNode[nodeID][configuration.Generation] = configuration
	s.desiredChanges.Notify(nodeID)
	return cloneNodeConfig(configuration), true, nil
}

// compileForwardingFragmentsLocked projects the rules which belong to this
// node into engine-native fragments. Each ingress protocol has an explicit
// compiler; unsupported shapes remain persisted and observable, but are never
// silently rewritten as another protocol. VLESS Reality is compiled only after
// the authoritative identity binding and node-local runtime material have
// passed the relationship checks in vless_runtime_repository.go.
func (s *Store) compileForwardingFragmentsLocked(nodeID string) ([]agentv1.ConfigurationFragment, error) {
	groupIDs := make([]string, 0)
	for groupID, members := range s.membersByGroup {
		member, exists := members[nodeID]
		if exists && member.RetiredAt == nil {
			groupIDs = append(groupIDs, groupID)
		}
	}
	sort.Strings(groupIDs)
	fragments := make([]agentv1.ConfigurationFragment, 0)
	for _, groupID := range groupIDs {
		network, configured := s.groupNetworks[groupID]
		if !configured || network.ConnectHost == "" {
			continue
		}
		tcpRules := make([]forwarding.Rule, 0)
		udpRules := make([]forwarding.Rule, 0)
		for _, stored := range s.forwardRules {
			if stored.EntryGroupID != groupID {
				continue
			}
			// Runtime health controls customer readiness, never desired config.
			// Recompilation must keep a temporarily unhealthy rule installed so
			// the protocol observer can verify its recovery.
			rule := s.forwardingBaseViewLocked(stored)
			if rule.EffectiveIngressProtocol() == forwarding.IngressVLESSReality {
				// forwardingViewLocked intentionally redacts SOCKS5 credentials;
				// restore only the non-public username for this internal compiler
				// call. Password remains in the private upstream side-map.
				rule.VLESSSOCKS5Username = stored.VLESSSOCKS5Username
				fragment, ready, err := s.compileVLESSRealityFragmentLocked(nodeID, rule)
				if err != nil {
					return nil, fmt.Errorf("compile VLESS forwarding rule %s: %w", rule.ID, err)
				}
				if ready {
					fragments = append(fragments, fragment)
				}
				continue
			}
			if eligibleForGOSTTCPDirect(rule) {
				tcpRules = append(tcpRules, rule)
				continue
			}
			if eligibleForGOSTUDPDirect(rule) && nodeSupportsGOSTUDPDirect(s.nodes[nodeID]) {
				udpRules = append(udpRules, rule)
			}
		}
		sort.Slice(tcpRules, func(left, right int) bool { return tcpRules[left].ID < tcpRules[right].ID })
		sort.Slice(udpRules, func(left, right int) bool { return udpRules[left].ID < udpRules[right].ID })
		for _, rule := range tcpRules {
			// ConnectHost is the public/customer address, not a safe local bind
			// address. The node-side listener is explicit about binding all local
			// interfaces; the advertised host remains in the customer projection.
			raw, err := gostconfig.CompileDirectTCP([]forwarding.Rule{rule}, "0.0.0.0")
			if err != nil {
				return nil, fmt.Errorf("compile forwarding rule %s: %w", rule.ID, err)
			}
			revision := rule.Revision
			if revision < 1 {
				revision = 1
			}
			fragments = append(fragments, agentv1.ConfigurationFragment{
				GroupID:       generations.ForwardingFragmentID(agentv1.EngineGOST, rule.ID),
				GroupRevision: agentv1.GroupRevision(revision),
				Engine:        agentv1.EngineGOST,
				Config:        raw,
			})
		}
		for _, rule := range udpRules {
			raw, err := gostconfig.CompileDirectUDP([]forwarding.Rule{rule}, "0.0.0.0")
			if err != nil {
				return nil, fmt.Errorf("compile UDP forwarding rule %s: %w", rule.ID, err)
			}
			revision := rule.Revision
			if revision < 1 {
				revision = 1
			}
			fragments = append(fragments, agentv1.ConfigurationFragment{
				GroupID:       generations.ForwardingFragmentID(agentv1.EngineGOST, rule.ID),
				GroupRevision: agentv1.GroupRevision(revision),
				Engine:        agentv1.EngineGOST,
				Config:        raw,
			})
		}
	}
	return fragments, nil
}

// compileVLESSRealityFragmentLocked is intentionally fail-closed. A missing
// identity, node capability, or runtime material means the rule remains
// pending and produces no executable fragment. The confidential values are
// used only in the returned bundle; they are never copied into a public rule
// projection or an audit event.
func (s *Store) compileVLESSRealityFragmentLocked(nodeID string, rule forwarding.Rule) (agentv1.ConfigurationFragment, bool, error) {
	if rule.Paused || !forwardingRuleCompilableStatus(rule.Status) ||
		rule.EffectiveIngressProtocol() != forwarding.IngressVLESSReality ||
		rule.Protocol != forwarding.ProtocolTCP || rule.EgressMode != forwarding.EgressDirect ||
		rule.IngressReadiness() != forwarding.IngressReady ||
		rule.PendingActivationReason() != forwarding.ActivationReasonVLESSRuntimeMaterialPending ||
		!nodeSupportsVLESSReality(s.nodes[nodeID]) {
		return agentv1.ConfigurationFragment{}, false, nil
	}
	if effectiveSpeed := effectiveVLESSSpeedLimit(s, rule); effectiveSpeed > 0 && !nodeSupportsTCSpeedLimit(s.nodes[nodeID]) {
		return agentv1.ConfigurationFragment{}, false, nil
	}
	bindingID, ok := s.activeVLESSBindingIDForRuleLocked(rule)
	if !ok {
		return agentv1.ConfigurationFragment{}, false, nil
	}
	material, err := s.runtimeMaterialForNodeLocked(nodeID, bindingID)
	if err != nil {
		return agentv1.ConfigurationFragment{}, false, nil
	}
	// The runtime repository re-checks these relationships under the same
	// store lock. Keep the explicit checks here as a defense against future
	// compiler callers bypassing that repository helper.
	if material.CustomerID != rule.CustomerID || material.ForwardingRuleID != rule.ID || material.BindingID != bindingID || material.NodeID != nodeID {
		return agentv1.ConfigurationFragment{}, false, nil
	}
	if rule.VLESSOutboundMode == forwarding.VLESSOutboundNative {
		options := importedconfig.NativeOptions{RuleID: rule.ID, CustomerID: rule.CustomerID, EntryGroupID: rule.EntryGroupID,
			UUID: material.CredentialUUID, PrivateKey: material.RealityPrivateKey, ListenAddress: "0.0.0.0", ListenPort: rule.ListenPort}
		if probe, exists := s.protocolProbes[rule.ID]; exists {
			options.ProbeUUID, options.EchoPort = probe.UUID, probe.EchoPort
		}
		raw, err := importedconfig.CompileNativeFragment(material.NativeConfig, options)
		if err != nil {
			return agentv1.ConfigurationFragment{}, false, nil
		}
		return agentv1.ConfigurationFragment{GroupID: generations.ForwardingFragmentID(agentv1.EngineXray, rule.ID), GroupRevision: agentv1.GroupRevision(rule.Revision), Engine: agentv1.EngineXray, Config: raw}, true, nil
	}
	destination := rule.RealityDestination
	if destination == "" {
		// Legacy snapshots predate the explicit decoy field. Preserve their
		// deterministic SNI:443 behavior during migration; never use the
		// forwarding target as the Reality decoy.
		destination = rule.RealityServerName + ":443"
	}
	profile := vlessconfig.RealityProfile{
		ServerName: rule.RealityServerName,
		Dest:       destination,
		PrivateKey: material.RealityPrivateKey,
		ShortIDs:   []string{rule.RealityShortID},
		Flow:       rule.EffectiveVLESSFlow(),
	}
	listener := vlessconfig.Listener{Address: "0.0.0.0", Port: rule.ListenPort}
	var socks5 *vlessconfig.SOCKS5Upstream
	if rule.VLESSOutboundMode == forwarding.VLESSOutboundSOCKS5 {
		stored, found := s.vlessSOCKS5UpstreamLocked(rule.ID)
		if !found || stored.Hostname != rule.VLESSSOCKS5Host || stored.Port != rule.VLESSSOCKS5Port {
			// A public rule projection is not sufficient to activate a SOCKS5
			// landing. Missing or mismatched private material fails closed.
			return agentv1.ConfigurationFragment{}, false, nil
		}
		socks5 = &vlessconfig.SOCKS5Upstream{
			Hostname: stored.Hostname, Port: stored.Port,
			Username: stored.Username, Password: stored.Password,
		}
	}
	var raw []byte
	if probe, configured := s.protocolProbes[rule.ID]; configured {
		probeIdentity := vlessconfig.ProbeIdentity{UUID: probe.UUID, EchoPort: probe.EchoPort}
		if socks5 != nil {
			raw, err = vlessconfig.CompileDirectWithProbeAndSOCKS5(rule, vlessconfig.Identity{UUID: material.CredentialUUID}, profile, listener, probeIdentity, *socks5)
		} else {
			raw, err = vlessconfig.CompileDirectWithProbe(rule, vlessconfig.Identity{UUID: material.CredentialUUID}, profile, listener, probeIdentity)
		}
	} else {
		if socks5 != nil {
			raw, err = vlessconfig.CompileDirectWithSOCKS5(rule, vlessconfig.Identity{UUID: material.CredentialUUID}, profile, listener, *socks5)
		} else {
			raw, err = vlessconfig.CompileDirect(rule, vlessconfig.Identity{UUID: material.CredentialUUID}, profile, listener)
		}
	}
	if err != nil {
		// A malformed or revoked runtime record must never be emitted. The
		// repository validates persisted records, so this is a defensive
		// fail-closed path rather than a user-visible compiler error.
		return agentv1.ConfigurationFragment{}, false, nil
	}
	revision := rule.Revision
	if revision < 1 {
		revision = 1
	}
	return agentv1.ConfigurationFragment{
		GroupID:       generations.ForwardingFragmentID(agentv1.EngineXray, rule.ID),
		GroupRevision: agentv1.GroupRevision(revision),
		Engine:        agentv1.EngineXray,
		Config:        raw,
	}, true, nil
}

// activeVLESSBindingIDForRuleLocked chooses one authoritative active identity
// deterministically. Provisioning currently permits one active tuple per
// customer/rule/endpoint pool; sorting protects bundle reproducibility if a
// future migration temporarily contains more than one endpoint pool.
func (s *Store) activeVLESSBindingIDForRuleLocked(rule forwarding.Rule) (string, bool) {
	ids := make([]string, 0)
	for id, record := range s.vlessBindings {
		binding := record.Binding
		if binding.State != "active" || binding.CustomerID != rule.CustomerID || binding.ForwardingRuleID != rule.ID {
			continue
		}
		pool, exists := s.endpointPools[binding.EndpointPoolID]
		if !exists || pool.GroupID != rule.EntryGroupID || pool.Protocol != "vless" {
			continue
		}
		if err := s.validateVLESSCredentialRecordLocked(record); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return "", false
	}
	sort.Strings(ids)
	return ids[0], true
}

func nodeSupportsVLESSReality(node nodes.Node) bool {
	for _, capability := range node.Capabilities {
		switch strings.ToLower(strings.TrimSpace(capability)) {
		case "xray", "xray-vless", "xray-vless-reality", "vless-reality", "vless_reality":
			return true
		}
	}
	// Older agents report engine versions but not the granular capability. An
	// advertised Xray version is sufficient because Reality is an Xray-native
	// transport in the supported engine line.
	for engine, version := range node.EngineVersions {
		if strings.EqualFold(strings.TrimSpace(engine), "xray") && strings.TrimSpace(version) != "" {
			return true
		}
	}
	return false
}

func nodeSupportsTCSpeedLimit(node nodes.Node) bool {
	for _, capability := range node.Capabilities {
		if capability == agentconfig.CapabilityLinuxTCSpeedLimit {
			return true
		}
	}
	return false
}

func effectiveVLESSSpeedLimit(s *Store, rule forwarding.Rule) int {
	speed := rule.SpeedLimitMbps
	if customer, ok := s.customers[rule.CustomerID]; ok && customer.SpeedLimitMbps > 0 && (speed <= 0 || customer.SpeedLimitMbps < speed) {
		speed = customer.SpeedLimitMbps
	}
	if speed < 0 {
		return 0
	}
	return speed
}

// recompileForwardingGroupsLocked is called inside the same repository write
// transaction as a rule mutation. It advances each affected node's desired
// NodeConfigGeneration only when the complete bundle hash changes.
func (s *Store) recompileForwardingGroupsLocked(groupIDs []string, now time.Time) error {
	seenGroups := make(map[string]struct{}, len(groupIDs))
	nodeIDs := make(map[string]struct{})
	for _, groupID := range groupIDs {
		if groupID == "" {
			continue
		}
		if _, seen := seenGroups[groupID]; seen {
			continue
		}
		seenGroups[groupID] = struct{}{}
		for nodeID, member := range s.membersByGroup[groupID] {
			if member.RetiredAt == nil {
				nodeIDs[nodeID] = struct{}{}
			}
		}
	}
	orderedNodes := make([]string, 0, len(nodeIDs))
	for nodeID := range nodeIDs {
		orderedNodes = append(orderedNodes, nodeID)
	}
	sort.Strings(orderedNodes)
	previousNodes := make(map[string]nodes.Node, len(orderedNodes))
	previousConfigs := make(map[string]map[int64]generations.NodeConfigGeneration, len(orderedNodes))
	for _, nodeID := range orderedNodes {
		previousNodes[nodeID] = s.nodes[nodeID]
		previousConfigs[nodeID] = maps.Clone(s.nodeConfigsByNode[nodeID])
	}
	for _, nodeID := range orderedNodes {
		if _, _, err := s.compileNodeConfigLocked(nodeID, now); err != nil {
			for _, rollbackNodeID := range orderedNodes {
				s.nodes[rollbackNodeID] = previousNodes[rollbackNodeID]
				if previousConfigs[rollbackNodeID] == nil {
					delete(s.nodeConfigsByNode, rollbackNodeID)
				} else {
					s.nodeConfigsByNode[rollbackNodeID] = previousConfigs[rollbackNodeID]
				}
			}
			return fmt.Errorf("recompile node %s after forwarding mutation: %w", nodeID, err)
		}
	}
	return nil
}

func eligibleForGOSTTCPDirect(rule forwarding.Rule) bool {
	if rule.Paused || !forwardingRuleCompilableStatus(rule.Status) || rule.EffectiveIngressProtocol() != forwarding.IngressTCP || rule.Protocol != forwarding.ProtocolTCP || rule.EgressMode != forwarding.EgressDirect {
		return false
	}
	// Do not publish a partial interpretation of advanced rule settings. They
	// need an engine adapter that can enforce them, so the rule stays pending.
	return rule.PendingActivationReason() == ""
}

func eligibleForGOSTUDPDirect(rule forwarding.Rule) bool {
	if rule.Paused || !forwardingRuleCompilableStatus(rule.Status) || rule.EffectiveIngressProtocol() != forwarding.IngressUDP || rule.Protocol != forwarding.ProtocolUDP || rule.EgressMode != forwarding.EgressDirect {
		return false
	}
	return rule.PendingActivationReason() == ""
}

func nodeSupportsGOSTUDPDirect(node nodes.Node) bool {
	for _, capability := range node.Capabilities {
		if capability == agentconfig.CapabilityGOSTUDPDirect {
			return true
		}
	}
	return false
}

// eligibleForGOSTDirect is retained for package-local callers and tests that
// previously referred to the TCP-only helper.
func eligibleForGOSTDirect(rule forwarding.Rule) bool { return eligibleForGOSTTCPDirect(rule) }

// A deployed rule remains executable during later node-bundle recompiles.
// Activation is a lifecycle projection; it must not make an otherwise valid
// listener disappear when an unrelated group or node change advances the
// desired configuration generation.
func forwardingRuleCompilableStatus(status forwarding.Status) bool {
	return status == forwarding.StatusPendingActivation || status == forwarding.StatusActive
}

func nodeSupportsUsageGeneration(node nodes.Node) bool {
	for _, capability := range node.Capabilities {
		if capability == agentv1.CapabilityUsageGeneration {
			return true
		}
	}
	return false
}
