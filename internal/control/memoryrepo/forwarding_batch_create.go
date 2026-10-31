package memoryrepo

import (
	"context"
	"fmt"
	"maps"
	"unicode/utf8"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/protocolprobe"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
)

// CreateForwardingRules reserves all ports and installs all private material
// before compiling each affected node once. A failed batch publishes nothing.
func (s *Store) CreateForwardingRules(ctx context.Context, input forwarding.BatchCreateInput, event audit.Event) (forwarding.BatchCreateResult, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := input.CreatedBy + "\x00forwarding.batch-create\x00" + input.IdempotencyKey
	var replay forwarding.BatchCreateResult
	if ok, err := s.replayBusinessLocked(key, input.RequestSHA256, &replay); ok || err != nil {
		if ok {
			for i := range replay.Items {
				replay.Items[i] = s.forwardingViewLocked(replay.Items[i])
			}
		}
		return replay, ok, err
	}
	if len(input.Rules) == 0 || len(input.Rules) > forwarding.MaxImportRules {
		return forwarding.BatchCreateResult{}, false, faults.ErrValidation
	}
	original := s.forwardRules
	working := maps.Clone(original)
	groups := make([]string, 0, len(input.Rules))
	added := make([]string, 0, len(input.Rules))
	autoNetworks := make([]string, 0)
	addedPools := make([]string, 0, len(input.Rules))
	addedBindings := make([]string, 0, len(input.Rules))
	committed := false
	defer func() {
		if committed {
			return
		}
		s.forwardRules = original
		delete(s.businessIdempotency, key)
		for _, id := range added {
			delete(s.autoRealityKeys, id)
			delete(s.protocolProbes, id)
			s.deleteVLESSSOCKS5UpstreamLocked(id)
		}
		for _, id := range autoNetworks {
			delete(s.groupNetworks, id)
		}
		for _, id := range addedPools {
			delete(s.endpointPools, id)
			delete(s.endpointMembers, id)
		}
		for _, id := range addedBindings {
			delete(s.vlessBindings, id)
		}
	}()
	after := s.panelResourceUsageLocked()
	after.Rules += len(input.Rules)
	if err := s.validatePanelGrowthLocked(after); err != nil {
		return forwarding.BatchCreateResult{}, false, err
	}
	// Stage the cloned map under the write lock so secret validation and bundle
	// compilation see the same rules. The deferred rollback restores the old map.
	s.forwardRules = working
	for i, prepared := range input.Rules {
		if err := ctx.Err(); err != nil {
			return forwarding.BatchCreateResult{}, false, err
		}
		item := cloneForwardingRule(prepared.Rule)
		if _, exists := working[item.ID]; exists {
			return forwarding.BatchCreateResult{}, false, faults.ErrConflict
		}
		if item.EffectiveIngressProtocol() != forwarding.IngressVLESSReality || item.VLESSOutboundMode != forwarding.VLESSOutboundSOCKS5 || item.EgressMode != forwarding.EgressDirect || item.Revision != 1 || !item.OwnedByAdministrator(event.ActorID) {
			return forwarding.BatchCreateResult{}, false, faults.ErrValidation
		}
		if !vlessruntime.RealityKeyMatchesPublicKey(prepared.RealityPrivateKey, item.RealityPublicKey) {
			return forwarding.BatchCreateResult{}, false, faults.ErrValidation
		}
		item.UserGroupDetached = false
		if err := s.prepareForwardingRuleAgainstLocked(&item, working); err != nil {
			return forwarding.BatchCreateResult{}, false, fmt.Errorf("第 %d 条: %w", i+1, err)
		}
		network, created, err := s.ensureEntryGroupNetworkLocked(item.EntryGroupID)
		if err != nil {
			return forwarding.BatchCreateResult{}, false, err
		}
		if created {
			autoNetworks = append(autoNetworks, item.EntryGroupID)
		}
		upstream, err := requestedVLESSSOCKS5Upstream(item, prepared.VLESSSOCKS5Password)
		if err != nil {
			return forwarding.BatchCreateResult{}, false, fmt.Errorf("第 %d 条: %w", i+1, err)
		}
		echoPort := prepared.ProtocolProbeEchoPort
		if echoPort == 0 {
			echoPort = protocolprobe.DefaultEchoPort
		}
		probe, err := gatewaymembership.NewProtocolProbeConfig(echoPort)
		if err != nil {
			return forwarding.BatchCreateResult{}, false, err
		}
		item.RuntimeRevision = 1
		item.ActivationReason = s.forwardingBaseViewLocked(item).ActivationReason
		view := s.forwardingViewLocked(item)
		item.Status, item.IngressStatus, item.Deployed = view.Status, view.IngressStatus, view.Deployed
		added = append(added, item.ID)
		working[item.ID] = item
		if err := s.setVLESSSOCKS5UpstreamLocked(item.ID, *upstream); err != nil {
			return forwarding.BatchCreateResult{}, false, err
		}
		s.autoRealityKeys[item.ID] = prepared.RealityPrivateKey
		s.protocolProbes[item.ID] = probe
		credential := cloneVLESSCredentialRecord(prepared.Credential)
		if credential.Binding.ForwardingRuleID != item.ID || credential.Binding.CustomerID != item.CustomerID || credential.Binding.State != vlessidentity.StateActive {
			return forwarding.BatchCreateResult{}, false, faults.ErrValidation
		}
		name := item.Name
		for len(name) > 96 {
			_, size := utf8.DecodeLastRuneInString(name)
			name = name[:len(name)-size]
		}
		pool, err := s.stageEndpointPoolLocked(endpoints.CreatePoolInput{ID: credential.Binding.EndpointPoolID,
			Name: name + " " + item.ID, GroupID: item.EntryGroupID, RuleID: item.ID, CreatedBy: event.ActorID, CreatedAt: event.CreatedAt,
			Mode: endpoints.ModeSingleServiceEndpoint, Protocol: "vless", Hostname: network.ConnectHost, Port: item.ListenPort, SelectionPolicy: endpoints.SelectionWeightedRoundRobin})
		if err != nil {
			return forwarding.BatchCreateResult{}, false, fmt.Errorf("第 %d 条: %w", i+1, err)
		}
		addedPools = append(addedPools, pool.ID)
		if err := s.validateNewVLESSCredentialLocked(credential, event.ActorID, false); err != nil {
			return forwarding.BatchCreateResult{}, false, fmt.Errorf("第 %d 条: %w", i+1, err)
		}
		s.vlessBindings[credential.Binding.ID] = credential
		addedBindings = append(addedBindings, credential.Binding.ID)
		groups = append(groups, item.EntryGroupID)
	}
	if err := ctx.Err(); err != nil {
		return forwarding.BatchCreateResult{}, false, err
	}
	s.forwardRules = working
	result := forwarding.BatchCreateResult{Created: len(added), Items: make([]forwarding.Rule, 0, len(added))}
	for _, id := range added {
		result.Items = append(result.Items, s.forwardingViewLocked(working[id]))
	}
	if err := s.recordBusinessLocked(key, input.RequestSHA256, event.ResourceID, result); err != nil {
		return forwarding.BatchCreateResult{}, false, err
	}
	if err := s.recompileForwardingGroupsLocked(groups, event.CreatedAt); err != nil {
		return forwarding.BatchCreateResult{}, false, err
	}
	s.appendAuditLocked(event)
	committed = true
	return result, false, nil
}
