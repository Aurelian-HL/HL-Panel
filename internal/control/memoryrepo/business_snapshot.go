package memoryrepo

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/hongle/hl-panel/internal/control/announcements"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/rulegroups"
	"github.com/hongle/hl-panel/internal/control/siteconfig"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
)

// Validate independent identities and customer references without altering any
// user-authored status. Device-group validation runs after base state is loaded.
func (s *Store) validateBusinessSnapshot() error {
	if siteErr := validateSiteState(s); siteErr != nil {
		return siteErr
	}
	ruleGroupNames := make(map[string]bool)
	for id, group := range s.ruleGroups {
		if id != group.ID || group.Revision < 1 || ruleGroupNames[strings.ToLower(group.Name)] {
			return errors.New("invalid persisted rule group")
		}
		if _, err := rulegroups.NormalizeRequest(rulegroups.Request{Name: group.Name, Description: group.Description, Revision: group.Revision}); err != nil {
			return errors.New("invalid persisted rule group")
		}
		ruleGroupNames[strings.ToLower(group.Name)] = true
	}
	groupNames := make(map[string]bool)
	for id, group := range s.userGroups {
		if id != group.ID || customers.ValidateStoredUserGroup(group) != nil || groupNames[strings.ToLower(group.Name)] {
			return errors.New("invalid persisted user group")
		}
		groupNames[strings.ToLower(group.Name)] = true
		if s.validateAuthorizedGroupsLocked(group.AllowedEntryGroupIDs, true) != nil || s.validateAuthorizedGroupsLocked(group.AllowedExitGroupIDs, false) != nil {
			return errors.New("invalid persisted user-group authorization")
		}
	}
	for _, group := range s.deviceGroups {
		if group.UserGroupID != "" {
			if _, found := s.userGroups[group.UserGroupID]; !found {
				return errors.New("persisted device group references missing user group")
			}
		}
	}
	usernames := make(map[string]bool)
	for id, customer := range s.customers {
		if id != customer.ID || customers.ValidateStoredCustomer(customer) != nil || usernames[strings.ToLower(customer.Username)] {
			return errors.New("invalid persisted customer")
		}
		usernames[strings.ToLower(customer.Username)] = true
		if _, found := s.userGroups[customer.UserGroupID]; customer.UserGroupID != "" && !found {
			return errors.New("persisted customer references missing user group")
		}
	}
	for id, network := range s.groupNetworks {
		if id != network.GroupID || network.Revision < 1 || !network.DirectPolicy.Valid() {
			return errors.New("invalid persisted group network")
		}
		if _, err := groupconfig.NormalizeRequest(groupconfig.Request{
			ConnectHost: network.ConnectHost, PortStart: network.PortStart, PortEnd: network.PortEnd, PortRanges: network.PortRanges,
			DirectPolicy: network.DirectPolicy, AllowDirect: network.AllowDirect,
			AllowedUserGroupIDs: network.AllowedUserGroupIDs, AllowedEntryGroupIDs: network.AllowedEntryGroupIDs,
			AllowedExitGroupIDs: network.AllowedExitGroupIDs, FallbackExitGroupID: network.FallbackExitGroupID,
			TrafficMultiplier: network.TrafficMultiplier, Revision: network.Revision - 1,
		}); err != nil {
			return errors.New("invalid persisted network policy")
		}
		if s.validateGroupNetworkLocked(network) != nil {
			return errors.New("invalid persisted network reference")
		}
	}
	bindings := make(map[string]bool)
	nodeBindings := make(map[string]bool)
	for id, rule := range s.forwardRules {
		if id != rule.ID || id == "" || rule.Revision < 1 || rule.ListenPort < 1 || rule.ListenPort > 65535 {
			return errors.New("invalid persisted forwarding rule")
		}
		adminOwned := rule.OwnerKind == forwarding.OwnerAdministrator
		_, customerFound := s.customers[rule.CustomerID]
		if adminOwned {
			customerFound = s.hasAdministratorSubjectLocked(rule)
		}
		if !customerFound || !adminOwned && (rule.OwnerKind != "" && rule.OwnerKind != forwarding.OwnerCustomer || rule.OwnerID != "" && rule.OwnerID != rule.CustomerID) {
			return errors.New("persisted rule references missing customer")
		}
		if rule.RuleGroupID != "" {
			if _, found := s.ruleGroups[rule.RuleGroupID]; !found {
				return errors.New("persisted rule references missing rule group")
			}
		}
		if rule.ActivationReason != "" && rule.ActivationReason != rule.PendingActivationReason() {
			return errors.New("invalid persisted forwarding activation reason")
		}
		// SOCKS5 username/password are private runtime material. The rule
		// projection intentionally omits both fields, so structural validation
		// must use the private upstream side-map instead of trusting a legacy
		// username that may still exist in an older snapshot.
		validatedSOCKS5Username := ""
		validatedSOCKS5Password := ""
		if rule.EffectiveIngressProtocol() == forwarding.IngressVLESSReality && rule.VLESSOutboundMode == forwarding.VLESSOutboundSOCKS5 {
			if upstream, exists := s.vlessSOCKS5Upstreams[rule.ID]; exists {
				validatedSOCKS5Username = upstream.Username
				if upstream.Password != "" {
					// Never put the real secret in a validation request or error.
					validatedSOCKS5Password = "snapshot-secret-present"
				}
			}
		}
		if _, err := forwarding.NormalizeRequest(forwarding.Request{
			Name: rule.Name, CustomerID: rule.CustomerID, OwnerKind: rule.OwnerKind, OwnerID: rule.OwnerID, RuleGroupID: rule.RuleGroupID,
			EntryGroupID: rule.EntryGroupID, ExitGroupID: rule.ExitGroupID,
			EgressMode: rule.EgressMode, VLESSOutboundMode: rule.VLESSOutboundMode,
			VLESSSOCKS5Host: rule.VLESSSOCKS5Host, VLESSSOCKS5Port: rule.VLESSSOCKS5Port,
			VLESSSOCKS5Username: validatedSOCKS5Username, VLESSSOCKS5Password: validatedSOCKS5Password, IngressProtocol: rule.EffectiveIngressProtocol(),
			VLESSFlow: rule.VLESSFlow, RealityServerName: rule.RealityServerName,
			RealityPublicKey: rule.RealityPublicKey, RealityShortID: rule.RealityShortID, RealityDestination: rule.RealityDestination,
			Protocol: rule.Protocol, ListenPort: rule.ListenPort, Targets: rule.Targets,
			SelectionPolicy: rule.SelectionPolicy, AcceptProxyProtocol: rule.AcceptProxyProtocol,
			SendProxyProtocol: rule.SendProxyProtocol, SpeedLimitMbps: rule.SpeedLimitMbps,
			IPLimit: rule.IPLimit, ConnectionLimit: rule.ConnectionLimit,
		}); err != nil {
			return errors.New("invalid persisted forwarding configuration")
		}
		if rule.EffectiveIngressProtocol() == forwarding.IngressVLESSReality && rule.VLESSOutboundMode == forwarding.VLESSOutboundSOCKS5 {
			upstream, exists := s.vlessSOCKS5Upstreams[rule.ID]
			// Username and password are private runtime material and are
			// intentionally absent from the public rule projection. The stable
			// binding that can be checked here is the normalized landing endpoint.
			if !exists || upstream.Hostname != rule.VLESSSOCKS5Host || upstream.Port != rule.VLESSSOCKS5Port {
				return errors.New("persisted VLESS SOCKS5 rule is missing private upstream")
			}
		} else if _, exists := s.vlessSOCKS5Upstreams[rule.ID]; exists {
			return errors.New("persisted VLESS SOCKS5 upstream is attached to a non-SOCKS5 rule")
		}
		network, found := s.groupNetworks[rule.EntryGroupID]
		customer := s.customers[rule.CustomerID]
		userGroup := s.userGroups[customer.UserGroupID]
		if !found || !network.ContainsPort(rule.ListenPort) || !adminOwned && !slices.Contains(userGroup.AllowedEntryGroupIDs, rule.EntryGroupID) {
			return errors.New("invalid persisted rule entry binding")
		}
		if !adminOwned && len(network.AllowedUserGroupIDs) > 0 && !slices.Contains(network.AllowedUserGroupIDs, customer.UserGroupID) {
			return errors.New("invalid persisted rule entry user-group authorization")
		}
		if rule.EgressMode == forwarding.EgressDirect && (!network.EffectiveDirectPolicy().AllowsDirect() || !adminOwned && !userGroup.AllowDirect) {
			return errors.New("invalid persisted rule direct authorization")
		}
		if rule.EgressMode == forwarding.EgressExitGroup && (!network.EffectiveDirectPolicy().AllowsExitGroup() || rule.EntryGroupID == rule.ExitGroupID || !adminOwned && !slices.Contains(userGroup.AllowedExitGroupIDs, rule.ExitGroupID) || !slices.Contains(network.AllowedExitGroupIDs, rule.ExitGroupID)) {
			return errors.New("invalid persisted rule exit authorization")
		}
		if rule.EgressMode == forwarding.EgressExitGroup {
			if exitNetwork, exists := s.groupNetworks[rule.ExitGroupID]; exists {
				if len(exitNetwork.AllowedEntryGroupIDs) > 0 && !slices.Contains(exitNetwork.AllowedEntryGroupIDs, rule.EntryGroupID) {
					return errors.New("invalid persisted rule reverse entry authorization")
				}
				if !adminOwned && len(exitNetwork.AllowedUserGroupIDs) > 0 && !slices.Contains(exitNetwork.AllowedUserGroupIDs, customer.UserGroupID) {
					return errors.New("invalid persisted rule exit user-group authorization")
				}
			}
		}
		binding := fmt.Sprintf("%s/%s/%d", rule.EntryGroupID, rule.EffectiveIngressProtocol(), rule.ListenPort)
		if bindings[binding] {
			return errors.New("duplicate persisted listener binding")
		}
		bindings[binding] = true
		for nodeID, member := range s.membersByGroup[rule.EntryGroupID] {
			if member.RetiredAt != nil {
				continue
			}
			listener := fmt.Sprintf("%s/%s/%d", nodeID, rule.EffectiveIngressProtocol(), rule.ListenPort)
			if nodeBindings[listener] {
				return errors.New("conflicting persisted forwarding listeners on shared node")
			}
			nodeBindings[listener] = true
		}
	}
	// SOCKS5 landing credentials are kept in a private side map. Every entry
	// must belong to a persisted rule; an orphan would otherwise survive rule
	// deletion and could later be attached to a different listener ID.
	for ruleID, upstream := range s.vlessSOCKS5Upstreams {
		if _, exists := s.forwardRules[ruleID]; !exists {
			return errors.New("invalid persisted VLESS SOCKS5 upstream rule")
		}
		if _, err := normalizeVLESSSOCKS5Upstream(ruleID, upstream); err != nil {
			return errors.New("invalid persisted VLESS SOCKS5 upstream")
		}
	}
	activeVLESSTuples := make(map[string]bool)
	for ruleID, privateKey := range s.autoRealityKeys {
		rule, exists := s.forwardRules[ruleID]
		if !exists || rule.EffectiveIngressProtocol() != forwarding.IngressVLESSReality ||
			!vlessruntime.RealityKeyMatchesPublicKey(privateKey, rule.RealityPublicKey) {
			return errors.New("invalid persisted automatic Reality material")
		}
	}
	for id, record := range s.vlessBindings {
		if id != record.Binding.ID || s.validateVLESSCredentialRecordLocked(record) != nil {
			return errors.New("invalid persisted VLESS identity binding")
		}
		if record.Binding.State == vlessidentity.StateActive {
			key := strings.Join([]string{record.Binding.CustomerID, record.Binding.ForwardingRuleID, record.Binding.EndpointPoolID}, "\x00")
			if activeVLESSTuples[key] {
				return errors.New("duplicate persisted active VLESS identity binding")
			}
			activeVLESSTuples[key] = true
		}
	}
	activeRuntimeBindings := make(map[string]bool)
	for id, material := range s.vlessRuntimeMaterials {
		if id != material.ID || s.validateVLESSRuntimeMaterialLocked(material) != nil {
			return errors.New("invalid persisted VLESS runtime material")
		}
		if material.State == vlessruntime.StateActive {
			key := strings.Join([]string{material.BindingID, material.NodeID}, "\x00")
			if activeRuntimeBindings[key] {
				return errors.New("duplicate persisted active VLESS runtime material")
			}
			activeRuntimeBindings[key] = true
		}
	}
	for key, mutation := range s.businessIdempotency {
		if key == "" || mutation.RequestSHA256 == "" || mutation.ResourceID == "" || !json.Valid(mutation.Result) || string(mutation.Result) == "null" {
			return errors.New("invalid persisted operation record")
		}
	}
	return nil
}

func validateSiteState(s *Store) error {
	if siteconfig.ValidateStored(s.siteSettings) != nil {
		return errors.New("invalid persisted site settings")
	}
	for id, item := range s.announcements {
		if id != item.ID || announcements.ValidateStored(item) != nil {
			return errors.New("invalid persisted announcement")
		}
	}
	return nil
}
