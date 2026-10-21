package memoryrepo

import (
	"context"
	"net"
	"strconv"
	"time"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/vlessconnection"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	provisioningvless "github.com/hongle/hl-panel/internal/provisioning/vless"
)

var _ vlessconnection.ConfigurationSource = (*Store)(nil)

func (s *Store) ResolveForwardingConfiguration(ctx context.Context, adminID, ruleID string) (vlessconnection.Configuration, error) {
	if err := ctx.Err(); err != nil {
		return vlessconnection.Configuration{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rule, exists := s.forwardRules[ruleID]
	if adminID == "" || !exists || (!rule.OwnedByAdministrator(adminID) && !s.importedRuleManagedByLocked(rule, adminID) && !s.delegatedRuleManagedByLocked(rule, adminID)) {
		return vlessconnection.Configuration{}, faults.ErrNotFound
	}
	if rule.ID != ruleID {
		return vlessconnection.Configuration{}, faults.ErrConflict
	}
	var record vlessidentity.CredentialRecord
	found := false
	for id, candidate := range s.vlessBindings {
		if candidate.Binding.ForwardingRuleID != rule.ID || candidate.Binding.State == vlessidentity.StateRevoked {
			continue
		}
		// Count all active records for this rule, including malformed or foreign
		// ones. Filtering by ownership first would hide an ambiguous identity.
		if found || candidate.Binding.State != vlessidentity.StateActive || candidate.Binding.ID != id {
			return vlessconnection.Configuration{}, faults.ErrConflict
		}
		record, found = candidate, true
	}
	if !found {
		return vlessconnection.Configuration{}, faults.ErrNotFound
	}
	if !s.hasNonCustomerSubjectLocked(rule) {
		customer, ok := s.customers[record.Binding.CustomerID]
		if !ok || customer.ID != record.Binding.CustomerID {
			return vlessconnection.Configuration{}, faults.ErrConflict
		}
	}
	pool := s.endpointPools[record.Binding.EndpointPoolID]
	if !s.vlessBindingOwnedByAdministratorLocked(record.Binding, adminID) ||
		pool.ID != record.Binding.EndpointPoolID || pool.Port != rule.ListenPort ||
		rule.RealityServerName == "" || rule.RealityPublicKey == "" || rule.RealityShortID == "" || rule.RealityDestination == "" ||
		s.validateVLESSCredentialRecordLocked(record) != nil {
		return vlessconnection.Configuration{}, faults.ErrConflict
	}
	profile := vlessProfile(pool, rule, record.CredentialUUID)
	profile.Endpoint.Name = rule.Name
	uri, err := provisioningvless.URI(profile)
	if err != nil {
		return vlessconnection.Configuration{}, faults.ErrConflict
	}
	customer := s.customers[rule.CustomerID]
	availability := forwarding.CustomerAvailability{Enabled: !customer.Disabled, ExpiresAt: customer.ExpiresAt,
		TrafficLimitBytes: customer.TrafficLimitBytes, UsedTrafficBytes: customer.TrafficUsedBytes}
	if rule.OwnerKind == forwarding.OwnerAdministrator || rule.IsUnassignedImport() {
		availability = forwarding.CustomerAvailability{Enabled: s.hasNonCustomerSubjectLocked(rule)}
	}
	ruleStatus := forwarding.DeriveStatus(rule, availability, time.Now().UTC())
	status := "inactive"
	switch ruleStatus {
	case forwarding.StatusPaused:
		status = "paused"
	case forwarding.StatusPendingActivation, forwarding.StatusActive:
		status = "configured"
	}
	return vlessconnection.Configuration{URI: uri, Name: rule.Name,
		Endpoint: net.JoinHostPort(pool.Hostname, strconv.Itoa(pool.Port)), BindingID: record.Binding.ID,
		RuleID: rule.ID, CustomerID: rule.CustomerID, EndpointPoolID: pool.ID,
		RuleRevision: rule.Revision, BindingRevision: record.Binding.Revision, Paused: rule.Paused,
		Status: status, RuleStatus: ruleStatus, ServiceReady: false, LiveReadinessChecked: false}, nil
}

func (v *ForwardingReadView) ResolveForwardingConfiguration(ctx context.Context, adminID, ruleID string) (vlessconnection.Configuration, error) {
	return v.state.ResolveForwardingConfiguration(ctx, adminID, ruleID)
}
