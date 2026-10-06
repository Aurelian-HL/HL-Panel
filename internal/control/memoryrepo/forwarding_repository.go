package memoryrepo

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
	provisioningvless "github.com/hongle/hl-panel/internal/provisioning/vless"
)

var _ forwarding.Repository = (*Store)(nil)

func (s *Store) MarkActivated(_ context.Context, ruleID string, event audit.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rule, ok := s.forwardRules[ruleID]
	if !ok {
		return faults.ErrNotFound
	}
	if rule.Paused || rule.Status == forwarding.StatusPaused || rule.Status == forwarding.StatusCustomerDisabled || rule.Status == forwarding.StatusCustomerExpired || rule.Status == forwarding.StatusQuotaExhausted {
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
	item := cloneForwardingRule(input.Rule)
	if _, exists := s.forwardRules[item.ID]; exists {
		return forwarding.Rule{}, false, faults.ErrConflict
	}
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
	result, replayed, err := s.saveForwardingRuleWithKeyLocked(key, input.RequestSHA256, item, input.RealityPrivateKey, upstream, event)
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
	if previous.OwnerKind == forwarding.OwnerAdministrator && (previous.OwnerKind != item.OwnerKind || previous.OwnerID != item.OwnerID) || previous.OwnerKind != forwarding.OwnerAdministrator && item.OwnerKind == forwarding.OwnerAdministrator {
		return forwarding.Rule{}, false, faults.ErrNotFound
	}
	if previous.Revision != input.ExpectedRevision || item.Revision != input.ExpectedRevision+1 {
		return forwarding.Rule{}, false, fmt.Errorf("%w: forwarding rule changed; reload before saving", faults.ErrConflict)
	}
	item.CreatedAt = previous.CreatedAt
	if item.ListenPort == 0 && item.EntryGroupID == previous.EntryGroupID && item.Protocol == previous.Protocol {
		item.ListenPort = previous.ListenPort
	}
	privateKey := ""
	if input.AutoReality && previous.EffectiveIngressProtocol() == forwarding.IngressVLESSReality && previous.IngressReadiness() == forwarding.IngressReady {
		item.RealityServerName, item.RealityDestination = previous.RealityServerName, previous.RealityDestination
		item.RealityPublicKey, item.RealityShortID = previous.RealityPublicKey, previous.RealityShortID
	} else if input.AutoReality {
		privateKey = input.RealityPrivateKey
		if privateKey == "" {
			return forwarding.Rule{}, false, fmt.Errorf("%w: automatic Reality target is not configured on the control plane", faults.ErrValidation)
		}
	}
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
	result, replayed, err := s.saveForwardingRuleWithKeyLocked(key, input.RequestSHA256, item, privateKey, upstream, event)
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
	adminOwned := item.OwnerKind == forwarding.OwnerAdministrator
	customer, found := s.customers[item.CustomerID]
	if adminOwned {
		if _, collision := s.customers[item.CustomerID]; collision {
			return fmt.Errorf("%w: administrator traffic subject collides with a customer", faults.ErrValidation)
		}
		found = s.hasAdministratorSubjectLocked(*item)
	} else if item.OwnerKind != "" && item.OwnerKind != forwarding.OwnerCustomer || item.OwnerID != "" && item.OwnerID != item.CustomerID {
		return fmt.Errorf("%w: invalid customer rule owner", faults.ErrValidation)
	}
	if !found {
		return fmt.Errorf("%w: customer does not exist", faults.ErrValidation)
	}
	userGroup, found := s.userGroups[customer.UserGroupID]
	if !adminOwned && (!found || !slices.Contains(userGroup.AllowedEntryGroupIDs, item.EntryGroupID)) {
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
	if !adminOwned && len(network.AllowedUserGroupIDs) > 0 && !slices.Contains(network.AllowedUserGroupIDs, customer.UserGroupID) {
		return fmt.Errorf("%w: entry group does not authorize the customer's user group", faults.ErrValidation)
	}
	if item.EgressMode == forwarding.EgressDirect {
		if !network.EffectiveDirectPolicy().AllowsDirect() || !adminOwned && !userGroup.AllowDirect {
			return fmt.Errorf("%w: direct forwarding requires user-group and entry-group authorization", faults.ErrValidation)
		}
	} else {
		if !network.EffectiveDirectPolicy().AllowsExitGroup() || item.EntryGroupID == item.ExitGroupID || !adminOwned && !slices.Contains(userGroup.AllowedExitGroupIDs, item.ExitGroupID) || !slices.Contains(network.AllowedExitGroupIDs, item.ExitGroupID) {
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
		if !adminOwned && len(exitNetwork.AllowedUserGroupIDs) > 0 && !slices.Contains(exitNetwork.AllowedUserGroupIDs, customer.UserGroupID) {
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
	return s.saveForwardingRuleWithKeyLocked(key, hash, item, "", nil, event)
}

func (s *Store) saveForwardingRuleWithKeyLocked(key, hash string, item forwarding.Rule, privateKey string, upstream *provisioningvless.SOCKS5Upstream, event audit.Event) (forwarding.Rule, bool, error) {
	// Keep the confidential username in the stored rule, while all public
	// projections (including idempotency replay data) are redacted below.
	storedItem := cloneForwardingRule(item)
	item = s.forwardingViewLocked(item)
	storedItem.Status = item.Status
	storedItem.IngressStatus = item.IngressStatus
	storedItem.Deployed = item.Deployed
	storedItem.ActivationReason = item.ActivationReason
	previous, hadPrevious := s.forwardRules[item.ID]
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
	previousKey, hadKey := s.autoRealityKeys[item.ID]
	if privateKey != "" {
		s.autoRealityKeys[item.ID] = privateKey
	} else if item.EffectiveIngressProtocol() != forwarding.IngressVLESSReality || item.RealityPublicKey != previous.RealityPublicKey {
		delete(s.autoRealityKeys, item.ID)
	}
	groups := []string{item.EntryGroupID}
	if hadPrevious {
		groups = append(groups, previous.EntryGroupID)
	}
	if err := s.recompileForwardingGroupsLocked(groups, event.CreatedAt); err != nil {
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
	item = s.forwardingBaseViewLocked(item)
	if item.EffectiveIngressProtocol() != forwarding.IngressTCP || item.Protocol != forwarding.ProtocolTCP || item.Paused ||
		(item.Status != forwarding.StatusPendingActivation && item.Status != forwarding.StatusActive) {
		return item
	}
	count := 0
	deployed := true
	for nodeID, member := range s.membersByGroup[item.EntryGroupID] {
		if member.RetiredAt != nil {
			continue
		}
		count++
		input, err := s.ruleNodeDeploymentInputLocked(item.ID, nodeID)
		if err != nil || !deploymentreceipts.Evaluate(input).ReceiptVerified {
			deployed = false
			break
		}
	}
	item.Deployed = deployed && count > 0
	if item.Deployed {
		item.Status = forwarding.StatusActive
		item.ActivationReason = ""
	} else {
		item.Status = forwarding.StatusPendingActivation
		item.ActivationReason = item.PendingActivationReason()
	}
	return item
}

func (s *Store) forwardingBaseViewLocked(item forwarding.Rule) forwarding.Rule {
	customer := s.customers[item.CustomerID]
	item = cloneForwardingRule(item)
	availability := forwarding.CustomerAvailability{Enabled: !customer.Disabled, ExpiresAt: customer.ExpiresAt, TrafficLimitBytes: customer.TrafficLimitBytes, UsedTrafficBytes: customer.TrafficUsedBytes}
	if item.OwnerKind == forwarding.OwnerAdministrator {
		availability = forwarding.CustomerAvailability{Enabled: s.hasAdministratorSubjectLocked(item)}
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
	return item
}

func (s *Store) hasAdministratorSubjectLocked(rule forwarding.Rule) bool {
	if rule.OwnerKind != forwarding.OwnerAdministrator || rule.OwnerID == "" || rule.CustomerID != forwarding.AdministratorSubjectID(rule.OwnerID) || len(rule.CustomerID) > 128 {
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
		if !nodeSupportsVLESSReality(s.nodes[nodeID]) {
			return false
		}
		material, err := s.runtimeMaterialForNodeLocked(nodeID, bindingID)
		if err != nil || material.CustomerID != rule.CustomerID || material.ForwardingRuleID != rule.ID {
			return false
		}
	}
	return activeMembers > 0
}

func cloneForwardingRule(item forwarding.Rule) forwarding.Rule {
	item.Targets = append([]forwarding.Target{}, item.Targets...)
	return item
}
