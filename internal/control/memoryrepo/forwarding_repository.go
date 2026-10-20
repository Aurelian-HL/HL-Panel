package memoryrepo

import (
	"context"
	"fmt"
	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"maps"
	"slices"
	"sort"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/importedconfig"
	"github.com/hongle/hl-panel/internal/control/protocolprobe"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
	provisioningvless "github.com/hongle/hl-panel/internal/provisioning/vless"
)

var _ forwarding.Repository = (*Store)(nil)

func (s *Store) MarkActivated(_ context.Context, ruleID string, expectedRevision int64, event audit.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.markActivatedLocked(ruleID, expectedRevision, event)
}

func (s *Store) markActivatedLocked(ruleID string, expectedRevision int64, event audit.Event) error {
	rule, ok := s.forwardRules[ruleID]
	if !ok {
		return faults.ErrNotFound
	}
	if rule.Revision != expectedRevision {
		return faults.ErrConflict
	}
	current := s.forwardingBaseViewLocked(rule)
	if current.Paused || (current.Status != forwarding.StatusPendingActivation && current.Status != forwarding.StatusActive) {
		return faults.ErrConflict
	}
	if s.vlessUDPDirectCapabilityMissingLocked(current) {
		return faults.ErrConflict
	}
	if rule.Deployed {
		return nil
	}
	if current.EffectiveIngressProtocol() != forwarding.IngressVLESSReality {
		return faults.ErrConflict
	}
	poolID, err := s.endpointPoolIDForRuleLocked(ruleID)
	if err != nil {
		return err
	}
	// The challenge and this write are separate operations. Revalidate the
	// current receipt, membership and bounded protocol lease under the lock;
	// an in-flight probe cannot activate a retired or reconfigured candidate.
	ready := false
	pool := s.endpointPools[poolID]
	for nodeID, member := range s.endpointMembers[poolID] {
		input, err := s.ruleNodeDeploymentInputBorrowedLocked(ruleID, nodeID)
		if err != nil {
			continue
		}
		evidence := gatewaymembership.DeploymentEvidence{RuleRevision: input.Rule.Revision, Status: deploymentreceipts.Evaluate(input)}
		if s.protocolHealth[poolID][nodeID].MatchesCurrentDeployment(pool, current, member, evidence, time.Now().UTC()) {
			ready = true
			break
		}
	}
	if !ready {
		return faults.ErrConflict
	}
	rule.Deployed = true
	rule.ActivationReason = ""
	rule.UpdatedAt = time.Now().UTC()
	s.forwardRules[ruleID] = rule
	event.ResourceType = "forwarding_rule"
	event.ResourceID = ruleID
	s.auditEvents = append(s.auditEvents, event)
	return nil
}

func (s *Store) ListForwardingRules(_ context.Context) ([]forwarding.Rule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]forwarding.Rule, 0, len(s.forwardRules))
	for _, item := range s.forwardRules {
		items = append(items, s.forwardingViewLocked(item))
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	return items, nil
}

func (s *Store) ForwardingRule(_ context.Context, id string) (forwarding.Rule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, found := s.forwardRules[id]
	if !found {
		return forwarding.Rule{}, faults.ErrNotFound
	}
	return s.forwardingViewLocked(item), nil
}

func (s *Store) CreateForwardingRule(_ context.Context, input forwarding.CreateInput, event audit.Event) (forwarding.Rule, bool, error) {
	if input.Rule.VLESSOutboundMode == forwarding.VLESSOutboundNative {
		return forwarding.Rule{}, false, fmt.Errorf("%w: native listeners require maintenance import", faults.ErrValidation)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := input.CreatedBy + "\x00forwarding.create\x00" + input.IdempotencyKey
	var replay forwarding.Rule
	if ok, err := s.replayBusinessLocked(key, input.RequestSHA256, &replay); ok || err != nil {
		if ok {
			replay = s.forwardingViewLocked(replay)
		}
		return replay, ok, err
	}
	if input.AutoReality && input.RealityPrivateKey == "" {
		return forwarding.Rule{}, false, fmt.Errorf("%w: automatic Reality target is not configured on the control plane", faults.ErrValidation)
	}
	after := s.panelResourceUsageLocked()
	after.Rules++
	if err := s.validatePanelGrowthLocked(after); err != nil {
		return forwarding.Rule{}, false, err
	}
	item := cloneForwardingRule(input.Rule)
	item.UserGroupDetached = false
	if _, exists := s.forwardRules[item.ID]; exists {
		return forwarding.Rule{}, false, faults.ErrConflict
	}
	item.UserGroupDetached = s.detachedRuleAuthorizedLocked(item)
	if err := s.prepareForwardingRuleLocked(&item); err != nil {
		return forwarding.Rule{}, false, err
	}
	_, autoNetwork, err := s.ensureEntryGroupNetworkLocked(item.EntryGroupID)
	if err != nil {
		return forwarding.Rule{}, false, err
	}
	if autoNetwork {
		event.Metadata = maps.Clone(event.Metadata)
		if event.Metadata == nil {
			event.Metadata = make(map[string]any)
		}
		event.Metadata["entry_network_defaulted"] = true
	}
	if input.RealityPrivateKey != "" && !vlessruntime.RealityKeyMatchesPublicKey(input.RealityPrivateKey, item.RealityPublicKey) {
		if autoNetwork {
			delete(s.groupNetworks, item.EntryGroupID)
		}
		return forwarding.Rule{}, false, faults.ErrValidation
	}
	upstream, err := requestedVLESSSOCKS5Upstream(item, input.VLESSSOCKS5Password)
	if err != nil {
		if autoNetwork {
			delete(s.groupNetworks, item.EntryGroupID)
		}
		return forwarding.Rule{}, false, err
	}
	result, replayed, err := s.saveForwardingRuleWithKeyLocked(key, input.RequestSHA256, item, input.RealityPrivateKey, upstream, input.ProtocolProbeEchoPort, event)
	if err != nil && autoNetwork {
		delete(s.groupNetworks, item.EntryGroupID)
	}
	return result, replayed, err
}

func (s *Store) UpdateForwardingRule(_ context.Context, input forwarding.UpdateInput, event audit.Event) (forwarding.Rule, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item := cloneForwardingRule(input.Rule)
	key := input.CreatedBy + "\x00forwarding.update\x00" + item.ID + "\x00" + input.IdempotencyKey
	var replay forwarding.Rule
	if ok, err := s.replayBusinessLocked(key, input.RequestSHA256, &replay); ok || err != nil {
		if ok {
			replay = s.forwardingViewLocked(replay)
		}
		return replay, ok, err
	}
	previous, found := s.forwardRules[item.ID]
	if !found {
		return forwarding.Rule{}, false, faults.ErrNotFound
	}
	if input.ExpectedCustomerID != "" && previous.CustomerID != input.ExpectedCustomerID {
		return forwarding.Rule{}, false, faults.ErrNotFound
	}
	if adminID := input.ExpectedAdministratorID; adminID != "" && !previous.OwnedByAdministrator(adminID) && !s.importedRuleManagedByLocked(previous, adminID) && !s.delegatedRuleManagedByLocked(previous, adminID) {
		return forwarding.Rule{}, false, faults.ErrNotFound
	}
	if previous.OwnerKind == forwarding.OwnerAdministrator && (previous.OwnerKind != item.OwnerKind || previous.OwnerID != item.OwnerID) || previous.OwnerKind != forwarding.OwnerAdministrator && item.OwnerKind == forwarding.OwnerAdministrator {
		return forwarding.Rule{}, false, faults.ErrNotFound
	}
	if previous.Revision != input.ExpectedRevision || item.Revision != input.ExpectedRevision+1 {
		return forwarding.Rule{}, false, fmt.Errorf("%w: forwarding rule changed; reload before saving", faults.ErrConflict)
	}
	if input.PreserveVLESSFlow &&
		previous.EffectiveIngressProtocol() == forwarding.IngressVLESSReality && item.EffectiveIngressProtocol() == forwarding.IngressVLESSReality {
		item.VLESSFlow, item.VLESSFlowExplicit = previous.VLESSFlow, previous.VLESSFlowExplicit
		if previous.Protocol == forwarding.ProtocolUDP && item.Protocol != forwarding.ProtocolUDP && previous.VLESSFlow == "" && !previous.VLESSFlowExplicit {
			item.VLESSFlowExplicit = true
		}
	}
	item.CreatedAt = previous.CreatedAt
	preserveRuleTraffic(&item, previous)
	if item.ListenPort == 0 && item.EntryGroupID == previous.EntryGroupID && item.Protocol == previous.Protocol {
		item.ListenPort = previous.ListenPort
	}
	if previous.VLESSOutboundMode == forwarding.VLESSOutboundNative || item.VLESSOutboundMode == forwarding.VLESSOutboundNative {
		if !forwarding.SameNativeRuntime(previous, item) || input.RealityPrivateKey != "" || input.VLESSSOCKS5Password != "" {
			return forwarding.Rule{}, false, fmt.Errorf("%w: imported connection parameters are immutable; name, group, pause and traffic quota may be edited", faults.ErrValidation)
		}
	}
	privateKey := ""
	if input.AutoReality && previous.EffectiveIngressProtocol() == forwarding.IngressVLESSReality && previous.IngressReadiness() == forwarding.IngressReady {
		if input.PreserveRealityTarget {
			item.RealityServerName, item.RealityDestination = previous.RealityServerName, previous.RealityDestination
		}
		item.RealityPublicKey, item.RealityShortID = previous.RealityPublicKey, previous.RealityShortID
	} else if input.AutoReality {
		privateKey = input.RealityPrivateKey
		if privateKey == "" {
			return forwarding.Rule{}, false, fmt.Errorf("%w: automatic Reality target is not configured on the control plane", faults.ErrValidation)
		}
	}
	item.UserGroupDetached = s.detachedRuleAuthorizedLocked(item)
	if err := s.prepareForwardingRuleLocked(&item); err != nil {
		return forwarding.Rule{}, false, err
	}
	_, autoNetwork, err := s.ensureEntryGroupNetworkLocked(item.EntryGroupID)
	if err != nil {
		return forwarding.Rule{}, false, err
	}
	if autoNetwork {
		event.Metadata = maps.Clone(event.Metadata)
		if event.Metadata == nil {
			event.Metadata = make(map[string]any)
		}
		event.Metadata["entry_network_defaulted"] = true
	}
	for _, pool := range s.endpointPools {
		if pool.RuleID == item.ID && (pool.GroupID != item.EntryGroupID || pool.Port != item.ListenPort ||
			(pool.Protocol == "vless" && item.EffectiveIngressProtocol() != forwarding.IngressVLESSReality) ||
			(pool.Protocol == "tcp" && item.EffectiveIngressProtocol() != forwarding.IngressTCP) ||
			(pool.Protocol == "socks5" && item.EffectiveIngressProtocol() != forwarding.IngressSOCKS5)) {
			if autoNetwork {
				delete(s.groupNetworks, item.EntryGroupID)
			}
			return forwarding.Rule{}, false, fmt.Errorf("%w: forwarding rule change would invalidate a bound service endpoint", faults.ErrConflict)
		}
	}
	if privateKey != "" && !vlessruntime.RealityKeyMatchesPublicKey(privateKey, item.RealityPublicKey) {
		if autoNetwork {
			delete(s.groupNetworks, item.EntryGroupID)
		}
		return forwarding.Rule{}, false, faults.ErrValidation
	}
	upstream, err := s.updatedVLESSSOCKS5Upstream(item, previous, input.VLESSSOCKS5Password)
	if err != nil {
		if autoNetwork {
			delete(s.groupNetworks, item.EntryGroupID)
		}
		return forwarding.Rule{}, false, err
	}
	result, replayed, err := s.saveForwardingRuleWithKeyLocked(key, input.RequestSHA256, item, privateKey, upstream, input.ProtocolProbeEchoPort, event)
	if err != nil && autoNetwork {
		delete(s.groupNetworks, item.EntryGroupID)
	}
	return result, replayed, err
}

func (s *Store) prepareForwardingRuleLocked(item *forwarding.Rule) error {
	return s.prepareForwardingRuleAgainstLocked(item, s.forwardRules)
}

func (s *Store) prepareForwardingRuleAgainstLocked(item *forwarding.Rule, rules map[string]forwarding.Rule) error {
	if item.RuleGroupID != "" {
		ruleGroup, found := s.ruleGroups[item.RuleGroupID]
		if !found {
			return fmt.Errorf("%w: rule group does not exist", faults.ErrValidation)
		}
		if item.OwnerKind == forwarding.OwnerAdministrator {
			if ruleGroup.OwnerAdministratorID != item.OwnerID {
				return fmt.Errorf("%w: rule group does not belong to this administrator", faults.ErrValidation)
			}
		} else if ruleGroup.OwnerAdministratorID != "" {
			return fmt.Errorf("%w: administrator rule group is not available to customer rules", faults.ErrValidation)
		}
	}
	adminOwned := item.OwnerKind == forwarding.OwnerAdministrator || item.IsUnassignedImport()
	customer, found := s.customers[item.CustomerID]
	if adminOwned {
		if _, collision := s.customers[item.CustomerID]; collision {
			return fmt.Errorf("%w: administrator traffic subject collides with a customer", faults.ErrValidation)
		}
		found = s.hasNonCustomerSubjectLocked(*item)
	} else if item.OwnerKind != "" && item.OwnerKind != forwarding.OwnerCustomer || item.OwnerID != "" && item.OwnerID != item.CustomerID {
		return fmt.Errorf("%w: invalid customer rule owner", faults.ErrValidation)
	}
	if !found {
		return fmt.Errorf("%w: customer does not exist", faults.ErrValidation)
	}
	userGroup, found := s.userGroups[customer.UserGroupID]
	detached := s.detachedRuleAuthorizedLocked(*item)
	if !adminOwned && !detached && (!found || !slices.Contains(userGroup.AllowedEntryGroupIDs, item.EntryGroupID)) {
		return fmt.Errorf("%w: customer is not authorized for this entry group", faults.ErrValidation)
	}
	if err := s.validateAuthorizedGroupsLocked([]string{item.EntryGroupID}, true); err != nil {
		return err
	}
	network, configured := s.groupNetworks[item.EntryGroupID]
	if !configured {
		var err error
		network, err = s.defaultEntryGroupNetworkLocked(item.EntryGroupID)
		if err != nil {
			return err
		}
		configured = true
	}
	if !adminOwned && !detached && len(network.AllowedUserGroupIDs) > 0 && !slices.Contains(network.AllowedUserGroupIDs, customer.UserGroupID) {
		return fmt.Errorf("%w: entry group does not authorize the customer's user group", faults.ErrValidation)
	}
	if item.EgressMode == forwarding.EgressDirect {
		if !network.EffectiveDirectPolicy().AllowsDirect() || !adminOwned && !detached && !userGroup.AllowDirect {
			return fmt.Errorf("%w: direct forwarding requires user-group and entry-group authorization", faults.ErrValidation)
		}
	} else {
		if !network.EffectiveDirectPolicy().AllowsExitGroup() || item.EntryGroupID == item.ExitGroupID || !adminOwned && !detached && !slices.Contains(userGroup.AllowedExitGroupIDs, item.ExitGroupID) || !slices.Contains(network.AllowedExitGroupIDs, item.ExitGroupID) {
			return fmt.Errorf("%w: exit group is not authorized by both user and entry group", faults.ErrValidation)
		}
		if err := s.validateAuthorizedGroupsLocked([]string{item.ExitGroupID}, false); err != nil {
			return err
		}
		exitNetwork, exists := s.groupNetworks[item.ExitGroupID]
		if !exists || exitNetwork.Revision < 1 {
			return fmt.Errorf("%w: configure the exit group network before adding rules", faults.ErrValidation)
		}
		if len(exitNetwork.AllowedEntryGroupIDs) > 0 && !slices.Contains(exitNetwork.AllowedEntryGroupIDs, item.EntryGroupID) {
			return fmt.Errorf("%w: exit group does not authorize this entry group", faults.ErrValidation)
		}
		if !adminOwned && !detached && len(exitNetwork.AllowedUserGroupIDs) > 0 && !slices.Contains(exitNetwork.AllowedUserGroupIDs, customer.UserGroupID) {
			return fmt.Errorf("%w: exit group does not authorize the customer's user group", faults.ErrValidation)
		}
		if err := s.validateExitGroupMembersLocked(item.ExitGroupID); err != nil {
			return err
		}
	}
	if !adminOwned && !item.Paused && customer.EffectiveStatus(time.Now().UTC()) != customers.StatusActive {
		return fmt.Errorf("%w: customer is disabled, expired or has exhausted the traffic quota", faults.ErrConflict)
	}
	count := 0
	for _, other := range rules {
		if other.CustomerID == item.CustomerID && other.ID != item.ID {
			count++
		}
	}
	if !adminOwned && customer.MaxRules > 0 && count >= customer.MaxRules {
		return fmt.Errorf("%w: customer rule quota is exhausted", faults.ErrConflict)
	}
	if item.ListenPort != 0 && !network.ContainsPort(item.ListenPort) {
		return fmt.Errorf("%w: listen port is outside the entry group range", faults.ErrValidation)
	}
	available := func(port int) bool {
		if item.EffectiveIngressProtocol() == forwarding.IngressUDP {
			for _, m := range s.reliableUDPTunnels {
				if m.Port == port && s.entryGroupsShareListenerLocked(m.ExitGroupID, item.EntryGroupID) {
					return false
				}
			}
		}
		for _, other := range rules {
			if other.ID != item.ID && forwardingListenerTransport(other.EffectiveIngressProtocol()) == forwardingListenerTransport(item.EffectiveIngressProtocol()) && other.ListenPort == port && s.entryGroupsShareListenerLocked(other.EntryGroupID, item.EntryGroupID) {
				return false
			}
		}
		return true
	}
	if item.ListenPort == 0 {
		for _, portRange := range network.EffectivePortRanges() {
			for port := portRange.Start; port <= portRange.End; port++ {
				if available(port) {
					item.ListenPort = port
					break
				}
			}
			if item.ListenPort != 0 {
				break
			}
		}
		if item.ListenPort == 0 {
			return fmt.Errorf("%w: no free port in the entry group range", faults.ErrConflict)
		}
	} else if !available(item.ListenPort) {
		return fmt.Errorf("%w: listen port is already reserved for this protocol in the entry group or a shared machine", faults.ErrConflict)
	}
	item.Deployed = false
	item.Status = forwarding.StatusPendingActivation
	return nil
}

func (s *Store) saveForwardingRuleLocked(key, hash string, item forwarding.Rule, event audit.Event) (forwarding.Rule, bool, error) {
	return s.saveForwardingRuleWithKeyLocked(key, hash, item, "", nil, protocolprobe.DefaultEchoPort, event)
}

func (s *Store) saveForwardingRuleWithKeyLocked(key, hash string, item forwarding.Rule, privateKey string, upstream *provisioningvless.SOCKS5Upstream, echoPort int, event audit.Event) (forwarding.Rule, bool, error) {
	if _, exists := s.forwardRules[item.ID]; !exists {
		after := s.panelResourceUsageLocked()
		after.Rules++
		if err := s.validatePanelGrowthLocked(after); err != nil {
			return forwarding.Rule{}, false, err
		}
	}
	previousProbe, hadProbe := s.protocolProbes[item.ID]
	probe := previousProbe
	if item.EffectiveIngressProtocol() == forwarding.IngressVLESSReality && !hadProbe {
		if echoPort == 0 {
			echoPort = protocolprobe.DefaultEchoPort
		}
		var err error
		probe, err = gatewaymembership.NewProtocolProbeConfig(echoPort)
		if err != nil {
			return forwarding.Rule{}, false, err
		}
	}
	// Keep the confidential username in the stored rule, while all public
	// projections (including idempotency replay data) are redacted below.
	storedItem := cloneForwardingRule(item)
	baseReason := s.forwardingBaseViewLocked(item).ActivationReason
	item = s.forwardingViewLocked(item)
	storedItem.Status = item.Status
	storedItem.IngressStatus = item.IngressStatus
	storedItem.Deployed = item.Deployed
	// Transient readiness explanations belong only to the public projection.
	// Persist the canonical configuration reason accepted by snapshot validation.
	storedItem.ActivationReason = baseReason
	previous, hadPrevious := s.forwardRules[item.ID]
	previousTunnel, hadPreviousTunnel := s.reliableUDPTunnels[item.ID]
	tunnel, err := s.prepareReliableUDPTunnelLocked(storedItem)
	if err != nil {
		return forwarding.Rule{}, false, err
	}
	previousUpstream, hadPreviousUpstream := s.vlessSOCKS5UpstreamLocked(item.ID)
	// Record the allocated port in the same audit transaction as the rule.
	event.Metadata = maps.Clone(event.Metadata)
	if event.Metadata == nil {
		event.Metadata = make(map[string]any)
	}
	event.Metadata["listen_port"] = item.ListenPort
	if err := s.recordBusinessLocked(key, hash, item.ID, item); err != nil {
		return forwarding.Rule{}, false, err
	}
	s.forwardRules[item.ID] = storedItem
	if upstream != nil {
		if err := s.setVLESSSOCKS5UpstreamLocked(item.ID, *upstream); err != nil {
			if hadPrevious {
				s.forwardRules[item.ID] = previous
			} else {
				delete(s.forwardRules, item.ID)
			}
			delete(s.businessIdempotency, key)
			return forwarding.Rule{}, false, err
		}
	} else {
		s.deleteVLESSSOCKS5UpstreamLocked(item.ID)
	}
	if storedItem.UsesReliableUDPTunnel() {
		s.reliableUDPTunnels[item.ID] = tunnel
	} else {
		delete(s.reliableUDPTunnels, item.ID)
	}
	previousKey, hadKey := s.autoRealityKeys[item.ID]
	if privateKey != "" {
		s.autoRealityKeys[item.ID] = privateKey
	} else if item.EffectiveIngressProtocol() != forwarding.IngressVLESSReality || item.RealityPublicKey != previous.RealityPublicKey {
		delete(s.autoRealityKeys, item.ID)
	}
	groups := []string{item.EntryGroupID, item.ExitGroupID}
	if hadPrevious {
		groups = append(groups, previous.EntryGroupID, previous.ExitGroupID)
	}
	// Include the probe-only identity in the very first bundle. Deferring this
	// until the periodic observer forces a second engine replacement.
	if probe.UUID != "" {
		s.protocolProbes[item.ID] = probe
	}
	if err := s.recompileForwardingGroupsLocked(groups, event.CreatedAt); err != nil {
		if hadPreviousTunnel {
			s.reliableUDPTunnels[item.ID] = previousTunnel
		} else {
			delete(s.reliableUDPTunnels, item.ID)
		}
		if hadProbe {
			s.protocolProbes[item.ID] = previousProbe
		} else {
			delete(s.protocolProbes, item.ID)
		}
		if hadPrevious {
			s.forwardRules[item.ID] = previous
		} else {
			delete(s.forwardRules, item.ID)
		}
		if hadKey {
			s.autoRealityKeys[item.ID] = previousKey
		} else {
			delete(s.autoRealityKeys, item.ID)
		}
		if hadPreviousUpstream {
			s.vlessSOCKS5Upstreams[item.ID] = previousUpstream
		} else {
			s.deleteVLESSSOCKS5UpstreamLocked(item.ID)
		}
		delete(s.businessIdempotency, key)
		return forwarding.Rule{}, false, err
	}
	s.appendAuditLocked(event)
	return item, false, nil
}

// requestedVLESSSOCKS5Upstream turns write-only request material into the
// private runtime record used by Xray bundle compilation. It deliberately
// returns nil for every non-SOCKS5 VLESS rule so old DIRECT rules cannot pick
// up a stale landing credential.
func requestedVLESSSOCKS5Upstream(rule forwarding.Rule, password string) (*provisioningvless.SOCKS5Upstream, error) {
	if rule.EffectiveIngressProtocol() != forwarding.IngressVLESSReality || rule.VLESSOutboundMode != forwarding.VLESSOutboundSOCKS5 {
		return nil, nil
	}
	if rule.VLESSSOCKS5Username == "" || password == "" {
		return nil, fmt.Errorf("%w: SOCKS5 username and password are both required", faults.ErrValidation)
	}
	return &provisioningvless.SOCKS5Upstream{
		Hostname: rule.VLESSSOCKS5Host, Port: rule.VLESSSOCKS5Port,
		Username: rule.VLESSSOCKS5Username, Password: password,
	}, nil
}

// updatedVLESSSOCKS5Upstream preserves write-only credentials when an update
// omits the password and retains the existing landing endpoint and username.
// Changed endpoints and credential replacements require a complete new pair.
func (s *Store) updatedVLESSSOCKS5Upstream(rule, previous forwarding.Rule, password string) (*provisioningvless.SOCKS5Upstream, error) {
	if rule.EffectiveIngressProtocol() != forwarding.IngressVLESSReality || rule.VLESSOutboundMode != forwarding.VLESSOutboundSOCKS5 {
		return nil, nil
	}
	if password != "" {
		return requestedVLESSSOCKS5Upstream(rule, password)
	}
	if previous.EffectiveIngressProtocol() != forwarding.IngressVLESSReality || previous.VLESSOutboundMode != forwarding.VLESSOutboundSOCKS5 ||
		rule.VLESSSOCKS5Host != previous.VLESSSOCKS5Host || rule.VLESSSOCKS5Port != previous.VLESSSOCKS5Port {
		return nil, fmt.Errorf("%w: changing the SOCKS5 endpoint requires a complete username and password", faults.ErrValidation)
	}
	old, ok := s.vlessSOCKS5UpstreamLocked(rule.ID)
	if !ok || old.Hostname != rule.VLESSSOCKS5Host || old.Port != rule.VLESSSOCKS5Port || old.Username == "" || old.Password == "" {
		return nil, fmt.Errorf("%w: stored SOCKS5 credentials are unavailable; provide a complete username and password", faults.ErrValidation)
	}
	if rule.VLESSSOCKS5Username != "" && rule.VLESSSOCKS5Username != old.Username {
		return nil, fmt.Errorf("%w: changing the SOCKS5 username requires a complete username and password", faults.ErrValidation)
	}
	return &old, nil
}

func (s *Store) forwardingViewLocked(item forwarding.Rule) forwarding.Rule {
	item = s.forwardingDeploymentViewLocked(item)
	item.Connection = s.ruleConnectionLocked(item, time.Now().UTC())
	return item
}

func (s *Store) forwardingDeploymentViewLocked(item forwarding.Rule) forwarding.Rule {
	item = s.forwardingBaseViewLocked(item)
	if item.EffectiveIngressProtocol() == forwarding.IngressVLESSReality && !item.Paused &&
		(item.Status == forwarding.StatusPendingActivation || item.Status == forwarding.StatusActive) {
		if s.vlessUDPDirectCapabilityMissingLocked(item) {
			item.Deployed = false
			item.Status = forwarding.StatusPendingActivation
			item.ActivationReason = forwarding.ActivationReasonUDPUnsupported
			return item
		}
		ready := false
		reason := forwarding.ActivationReasonNoAvailableNodes
		now := time.Now().UTC()
		for id, pool := range s.endpointPools {
			if pool.RuleID != item.ID {
				continue
			}
			state, err := s.gatewayMembershipStateLocked(id)
			if err == nil && gatewaymembership.ReadyCandidateCount(state, now) > 0 {
				ready = true
				break
			}
			if err == nil {
				if current := gatewaymembership.UnreadyReason(state, now); current != "" {
					reason = current
				}
			}
		}
		item.Deployed = item.Deployed && ready
		if !item.Deployed {
			item.Status = forwarding.StatusPendingActivation
			if item.ActivationReason == "" {
				if ready {
					reason = forwarding.ActivationReasonProtocolVerificationPending
				}
				item.ActivationReason = reason
			}
		}
		return item
	}
	isTCP := item.EffectiveIngressProtocol() == forwarding.IngressTCP && item.Protocol == forwarding.ProtocolTCP
	isUDP := item.EffectiveIngressProtocol() == forwarding.IngressUDP && item.Protocol == forwarding.ProtocolUDP
	if !isTCP && !isUDP || item.Paused ||
		(item.Status != forwarding.StatusPendingActivation && item.Status != forwarding.StatusActive) {
		return item
	}
	count := 0
	deployed := true
	udpCapabilityMissing := false
	groupIDs := []string{item.EntryGroupID}
	if item.UsesReliableUDPTunnel() {
		groupIDs = append(groupIDs, item.ExitGroupID)
	}
	for _, groupID := range groupIDs {
		groupCount := 0
		for nodeID, member := range s.membersByGroup[groupID] {
			if member.RetiredAt != nil {
				continue
			}
			count++
			groupCount++
			if (isUDP && !nodeSupportsGOSTUDPDirect(s.nodes[nodeID])) ||
				(item.UsesReliableUDPTunnel() && !nodeSupportsReliableUDP(s.nodes[nodeID])) {
				deployed = false
				udpCapabilityMissing = true
				break
			}
			input, err := s.ruleNodeDeploymentInputBorrowedLocked(item.ID, nodeID)
			if err != nil || !deploymentreceipts.Evaluate(input).ReceiptVerified {
				deployed = false
				break
			}
		}
		if groupCount == 0 {
			deployed = false
		}
	}
	item.Deployed = deployed && count > 0
	if item.Deployed {
		item.Status = forwarding.StatusActive
		item.ActivationReason = ""
	} else {
		item.Status = forwarding.StatusPendingActivation
		item.ActivationReason = item.PendingActivationReason()
		if udpCapabilityMissing {
			item.ActivationReason = forwarding.ActivationReasonUDPUnsupported
		}
	}
	return item
}

func (s *Store) forwardingBaseViewLocked(item forwarding.Rule) forwarding.Rule {
	customer := s.customers[item.CustomerID]
	item = cloneForwardingRule(item)
	item.Connection = nil // Live observations never enter configuration or revisions.
	availability := forwarding.CustomerAvailability{Enabled: !customer.Disabled, ExpiresAt: customer.ExpiresAt, TrafficLimitBytes: customer.TrafficLimitBytes, UsedTrafficBytes: customer.TrafficUsedBytes}
	if item.OwnerKind == forwarding.OwnerAdministrator || item.IsUnassignedImport() {
		availability = forwarding.CustomerAvailability{Enabled: s.hasNonCustomerSubjectLocked(item)}
	}
	item.Status = forwarding.DeriveStatus(item, availability, time.Now().UTC())
	// A VLESS Reality rule remains visibly pending until every active member of
	// its entry group has a matching node-local runtime material record. Public
	// projections never expose the UUID or private key; they only carry this
	// bounded reason while activation is incomplete.
	if item.Deployed {
		item.ActivationReason = ""
	} else {
		item.ActivationReason = item.PendingActivationReason()
		if item.ActivationReason == forwarding.ActivationReasonVLESSRuntimeMaterialPending && s.vlessRuntimeMaterialReadyLocked(item) {
			item.ActivationReason = ""
		}
	}
	// SOCKS5 credentials are runtime-only material. Keep the endpoint host and
	// port visible for operator context, but never expose the username through
	// list/get/import/export projections. The compiler reads the stored rule and
	// private side-map directly when building an Xray bundle.
	item.VLESSSOCKS5Username = ""
	if item.VLESSOutboundMode == forwarding.VLESSOutboundNative {
		if s.forwardingProjection != nil {
			summary := s.forwardingProjection.summaries[item.ID]
			item.OutboundSummary = &summary
			return item
		}
		item.OutboundSummary = &forwarding.OutboundSummary{Mode: "UNKNOWN"}
		for _, material := range s.vlessRuntimeMaterials {
			if material.ForwardingRuleID == item.ID && len(material.NativeConfig) > 0 {
				mode, host, port := importedconfig.NativeOutboundSummary(material.NativeConfig)
				item.OutboundSummary = &forwarding.OutboundSummary{Mode: mode, Host: host, Port: port}
				break
			}
		}
	}
	return item
}

func (s *Store) hasAdministratorSubjectLocked(rule forwarding.Rule) bool {
	if rule.OwnerKind != forwarding.OwnerAdministrator || rule.OwnerID == "" || (rule.CustomerID != forwarding.AdministratorSubjectID(rule.OwnerID) && !s.importedRuleManagedByLocked(rule, rule.OwnerID)) || len(rule.CustomerID) > 128 {
		return false
	}
	for _, admin := range s.adminsByUsername {
		if admin.ID == rule.OwnerID {
			return true
		}
	}
	return false
}

// vlessRuntimeMaterialReadyLocked reports whether a complete VLESS Reality
// rule can be emitted for the whole active entry group. A customer receives a
// single stable endpoint, so partially provisioned groups must remain pending
// instead of activating only a subset of nodes.
func (s *Store) vlessRuntimeMaterialReadyLocked(rule forwarding.Rule) bool {
	if rule.EffectiveIngressProtocol() != forwarding.IngressVLESSReality || rule.IngressReadiness() != forwarding.IngressReady {
		return false
	}
	bindingID, ok := s.activeVLESSBindingIDForRuleLocked(rule)
	if !ok {
		return false
	}
	activeMembers := 0
	for nodeID, member := range s.membersByGroup[rule.EntryGroupID] {
		if member.RetiredAt != nil {
			continue
		}
		activeMembers++
		if !nodeSupportsVLESSReality(s.nodes[nodeID]) ||
			(requiresVLESSUDPDirectCapability(rule) && !nodeSupportsVLESSUDPDirect(s.nodes[nodeID])) {
			return false
		}
		material, err := s.runtimeMaterialForNodeLocked(nodeID, bindingID)
		if err != nil || material.CustomerID != rule.CustomerID || material.ForwardingRuleID != rule.ID {
			return false
		}
	}
	return activeMembers > 0
}

func (s *Store) vlessUDPDirectCapabilityMissingLocked(rule forwarding.Rule) bool {
	if !requiresVLESSUDPDirectCapability(rule) {
		return false
	}
	for nodeID, member := range s.membersByGroup[rule.EntryGroupID] {
		if member.RetiredAt == nil && !nodeSupportsVLESSUDPDirect(s.nodes[nodeID]) {
			return true
		}
	}
	return false
}

func cloneForwardingRule(item forwarding.Rule) forwarding.Rule {
	item.Targets = append([]forwarding.Target{}, item.Targets...)
	if item.Connection != nil {
		copy := *item.Connection
		if copy.SampledAt != nil {
			at := *copy.SampledAt
			copy.SampledAt = &at
		}
		item.Connection = &copy
	}
	return item
}
