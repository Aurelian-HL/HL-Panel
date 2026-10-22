package memoryrepo

import (
	"encoding/json"
	"fmt"
	"iter"
	"maps"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	agentconfig "github.com/hongle/hl-panel/internal/agent/config"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/forwarding/gostconfig"
	"github.com/hongle/hl-panel/internal/control/forwarding/vlessconfig"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/importedconfig"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
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
// Revision 4 negotiates authenticated QUIC/KCP fragments and private TLS material.
const NodeBundleCompilerRevision = 4

func (s *Store) compileNodeConfigLocked(nodeID string, now time.Time) (generations.NodeConfigGeneration, bool, error) {
	return s.compileNodeConfigWithRealityKeysLocked(nodeID, now, nil)
}

func (s *Store) compileNodeConfigWithRealityKeysLocked(nodeID string, now time.Time, keys *compilationRealityKeys) (generations.NodeConfigGeneration, bool, error) {
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
	forwardingFragments, err := s.compileForwardingFragmentsWithRealityKeysLocked(nodeID, keys)
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
			if rule.UsesReliableUDPTunnel() {
				member, ok := s.membersByGroup[rule.EntryGroupID][nodeID]
				if !ok || member.RetiredAt != nil {
					continue
				}
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
	canonicalBundle, err := canonicalNodeBundle(agentv1.ConfigurationBundle{SchemaVersion: 1, Fragments: fragments})
	if err != nil {
		return generations.NodeConfigGeneration{}, false, fmt.Errorf("compile node bundle: %w", err)
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
	return s.compileForwardingFragmentsWithRealityKeysLocked(nodeID, nil)
}

func (s *Store) compileForwardingFragmentsWithRealityKeysLocked(nodeID string, keys *compilationRealityKeys) ([]agentv1.ConfigurationFragment, error) {
	groupIDs := make([]string, 0)
	for groupID, members := range s.membersByGroup {
		member, exists := members[nodeID]
		if exists && member.RetiredAt == nil {
			groupIDs = append(groupIDs, groupID)
		}
	}
	sort.Strings(groupIDs)
	fragments, err := s.compileReliableUDPFragmentsLocked(nodeID)
	if err != nil {
		return nil, err
	}
	var inputs *nodeCompilationInputs
	for _, groupID := range groupIDs {
		network, configured := s.groupNetworks[groupID]
		if !configured || network.ConnectHost == "" {
			continue
		}
		tcpRules := make([]forwarding.Rule, 0)
		udpRules := make([]forwarding.Rule, 0)
		for _, stored := range s.forwardRules {
			if stored.EntryGroupID != groupID || stored.UsesReliableUDPTunnel() {
				continue
			}
			// Runtime health controls customer readiness, never desired config.
			// Recompilation must keep a temporarily unhealthy rule installed so
			// the protocol observer can verify its recovery.
			rule := cloneForwardingRule(stored)
			rule.Connection = nil
			rule.Status = s.forwardingStatusLocked(stored, time.Now().UTC())
			if rule.EffectiveIngressProtocol() == forwarding.IngressVLESSReality {
				if inputs == nil {
					inputs = s.nodeCompilationInputsLocked(nodeID)
					inputs.realityKeys = keys
				}
				fragment, ready, err := s.compileVLESSRealityFragmentWithInputsLocked(nodeID, rule, inputs)
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
	return s.compileVLESSRealityFragmentWithInputsLocked(nodeID, rule, nil)
}

// These indexes live only for this node compilation under the store lock.
// They skip unrelated records without caching authorization. The optional
// cryptographic memo belongs to the same lock-held batch; neither can outlive
// a revoke, key rotation or membership mutation.
type nodeCompilationInputs struct {
	bindingsByRule     map[string][]string
	materialsByBinding map[string][]string
	realityKeys        *compilationRealityKeys
}

func (s *Store) nodeCompilationInputsLocked(nodeID string) *nodeCompilationInputs {
	inputs := &nodeCompilationInputs{
		bindingsByRule:     make(map[string][]string),
		materialsByBinding: make(map[string][]string),
	}
	for id, binding := range s.vlessBindings {
		ruleID := binding.Binding.ForwardingRuleID
		inputs.bindingsByRule[ruleID] = append(inputs.bindingsByRule[ruleID], id)
	}
	for id, material := range s.vlessRuntimeMaterials {
		if material.NodeID == nodeID {
			inputs.materialsByBinding[material.BindingID] = append(inputs.materialsByBinding[material.BindingID], id)
		}
	}
	return inputs
}

func (s *Store) compilationBindingsLocked(ids []string) iter.Seq2[string, vlessidentity.CredentialRecord] {
	return func(yield func(string, vlessidentity.CredentialRecord) bool) {
		for _, id := range ids {
			if !yield(id, s.vlessBindings[id]) {
				return
			}
		}
	}
}

func (s *Store) compilationMaterialsLocked(ids []string) iter.Seq[vlessruntime.Material] {
	return func(yield func(vlessruntime.Material) bool) {
		for _, id := range ids {
			if !yield(s.vlessRuntimeMaterials[id]) {
				return
			}
		}
	}
}

func (s *Store) compileVLESSRealityFragmentWithInputsLocked(nodeID string, rule forwarding.Rule, inputs *nodeCompilationInputs) (agentv1.ConfigurationFragment, bool, error) {
	if rule.Paused || !forwardingRuleCompilableStatus(rule.Status) ||
		rule.EffectiveIngressProtocol() != forwarding.IngressVLESSReality ||
		(rule.Protocol != forwarding.ProtocolTCP && rule.Protocol != forwarding.ProtocolUDP) || rule.EgressMode != forwarding.EgressDirect ||
		rule.IngressReadiness() != forwarding.IngressReady ||
		rule.PendingActivationReason() != forwarding.ActivationReasonVLESSRuntimeMaterialPending ||
		!nodeSupportsVLESSReality(s.nodes[nodeID]) ||
		(requiresVLESSUDPDirectCapability(rule) && !nodeSupportsVLESSUDPDirect(s.nodes[nodeID])) {
		return agentv1.ConfigurationFragment{}, false, nil
	}
	if effectiveSpeed := effectiveVLESSSpeedLimit(s, rule); effectiveSpeed > 0 && !nodeSupportsTCSpeedLimit(s.nodes[nodeID]) {
		return agentv1.ConfigurationFragment{}, false, nil
	}
	candidates := maps.All(s.vlessBindings)
	if inputs != nil {
		candidates = s.compilationBindingsLocked(inputs.bindingsByRule[rule.ID])
	}
	bindingID, ok := s.activeVLESSBindingIDFromCandidatesLocked(rule, candidates)
	if !ok {
		return agentv1.ConfigurationFragment{}, false, nil
	}
	materials := maps.Values(s.vlessRuntimeMaterials)
	if inputs != nil {
		materials = s.compilationMaterialsLocked(inputs.materialsByBinding[bindingID])
	}
	var keys *compilationRealityKeys
	if inputs != nil {
		keys = inputs.realityKeys
	}
	material, err := s.runtimeMaterialFromCandidatesWithRealityKeysLocked(nodeID, bindingID, materials, keys)
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
			UUID: material.CredentialUUID, PrivateKey: material.RealityPrivateKey, Protocol: string(rule.Protocol), ListenAddress: "0.0.0.0", ListenPort: rule.ListenPort}
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
// customer/rule/endpoint pool; key ordering protects bundle reproducibility if a
// future migration temporarily contains more than one endpoint pool.
func (s *Store) activeVLESSBindingIDForRuleLocked(rule forwarding.Rule) (string, bool) {
	return s.activeVLESSBindingIDFromCandidatesLocked(rule, maps.All(s.vlessBindings))
}

func (s *Store) activeVLESSBindingIDFromCandidatesLocked(rule forwarding.Rule, candidates iter.Seq2[string, vlessidentity.CredentialRecord]) (string, bool) {
	var selected string
	found := false
	for id, record := range candidates {
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
		if !found || id < selected {
			selected, found = id, true
		}
	}
	return selected, found
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

func requiresVLESSUDPDirectCapability(rule forwarding.Rule) bool {
	return rule.EffectiveIngressProtocol() == forwarding.IngressVLESSReality &&
		rule.Protocol == forwarding.ProtocolUDP && rule.VLESSOutboundMode == forwarding.VLESSOutboundDirect
}

func nodeSupportsVLESSUDPDirect(node nodes.Node) bool {
	if !agentVersionAtLeast(node.AgentVersion, [3]uint64{0, 1, 117}) ||
		node.EngineVersions["xray"] != "26.7.28" {
		return false
	}
	for _, capability := range node.Capabilities {
		if capability == agentconfig.CapabilityXrayVLESSUDPDirect {
			return true
		}
	}
	return false
}

func agentVersionAtLeast(version string, minimum [3]uint64) bool {
	raw, ok := strings.CutPrefix(version, "v")
	if !ok {
		return false
	}
	parts := strings.Split(raw, ".")
	if len(parts) != len(minimum) {
		return false
	}
	for i, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return false
		}
		for _, digit := range part {
			if digit < '0' || digit > '9' {
				return false
			}
		}
		value, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return false
		}
		if value != minimum[i] {
			return value > minimum[i]
		}
	}
	return true
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
	for _, groupID := range s.reliableUDPDependentGroupsLocked(groupIDs) {
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
	// Only a multi-node batch can reuse a key pair. Keep the memo local to
	// this lock-held call; no result may survive a mutation or rollback.
	var keys *compilationRealityKeys
	if len(orderedNodes) > 1 {
		keys = &compilationRealityKeys{}
	}
	for _, nodeID := range orderedNodes {
		if _, _, err := s.compileNodeConfigWithRealityKeysLocked(nodeID, now, keys); err != nil {
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
