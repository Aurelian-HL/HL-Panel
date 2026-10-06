package memoryrepo

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
	provisioningvless "github.com/hongle/hl-panel/internal/provisioning/vless"
)

var _ vlessidentity.Repository = (*Store)(nil)

func (s *Store) Provision(_ context.Context, input vlessidentity.ProvisionInput, event audit.Event) (vlessidentity.Binding, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := vlessMutationKey(input.CreatedBy, "provision", input.IdempotencyKey)
	var replay vlessidentity.Binding
	if replayed, err := s.replayBusinessLocked(key, input.RequestSHA256, &replay); err != nil || replayed {
		return replay, replayed, err
	}
	if input.CreatedBy == "" || input.IdempotencyKey == "" || input.RequestSHA256 == "" || input.ExpectedRevision != 0 {
		return vlessidentity.Binding{}, false, fmt.Errorf("%w: invalid VLESS provision mutation", faults.ErrValidation)
	}
	record := cloneVLESSCredentialRecord(input.Record)
	if record.Binding.Revision != 1 {
		return vlessidentity.Binding{}, false, fmt.Errorf("%w: initial VLESS binding revision must be one", faults.ErrValidation)
	}
	if _, exists := s.vlessBindings[record.Binding.ID]; exists {
		return vlessidentity.Binding{}, false, faults.ErrConflict
	}
	if !s.vlessBindingOwnedByAdministratorLocked(record.Binding, input.CreatedBy) {
		return vlessidentity.Binding{}, false, faults.ErrNotFound
	}
	for _, existing := range s.vlessBindings {
		if existing.Binding.State == vlessidentity.StateActive && sameVLESSTuple(existing.Binding, record.Binding) {
			return vlessidentity.Binding{}, false, fmt.Errorf("%w: an active VLESS binding already exists for this customer, rule, and endpoint", faults.ErrConflict)
		}
	}
	if err := s.validateVLESSCredentialRecordLocked(record); err != nil {
		return vlessidentity.Binding{}, false, err
	}
	public := cloneVLESSBinding(record.Binding)
	if err := s.recordBusinessLocked(key, input.RequestSHA256, public.ID, public); err != nil {
		return vlessidentity.Binding{}, false, err
	}
	s.vlessBindings[public.ID] = record
	if err := s.recompileForwardingGroupsLocked([]string{s.forwardRules[public.ForwardingRuleID].EntryGroupID}, public.CreatedAt); err != nil {
		delete(s.vlessBindings, public.ID)
		delete(s.businessIdempotency, key)
		return vlessidentity.Binding{}, false, err
	}
	s.appendAuditLocked(event)
	return public, false, nil
}

func (s *Store) List(_ context.Context) ([]vlessidentity.Binding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]vlessidentity.Binding, 0, len(s.vlessBindings))
	for _, record := range s.vlessBindings {
		items = append(items, cloneVLESSBinding(record.Binding))
	}
	sort.Slice(items, func(left, right int) bool {
		if items[left].CreatedAt.Equal(items[right].CreatedAt) {
			return items[left].ID < items[right].ID
		}
		return items[left].CreatedAt.Before(items[right].CreatedAt)
	})
	return items, nil
}

func (s *Store) ListForAdministrator(_ context.Context, administratorID string) ([]vlessidentity.Binding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]vlessidentity.Binding, 0)
	for _, record := range s.vlessBindings {
		if s.vlessBindingOwnedByAdministratorLocked(record.Binding, administratorID) {
			items = append(items, cloneVLESSBinding(record.Binding))
		}
	}
	sort.Slice(items, func(left, right int) bool {
		if items[left].CreatedAt.Equal(items[right].CreatedAt) {
			return items[left].ID < items[right].ID
		}
		return items[left].CreatedAt.Before(items[right].CreatedAt)
	})
	return items, nil
}

func (s *Store) CredentialForAdministrator(_ context.Context, administratorID, ruleID string) (vlessidentity.CredentialRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, record := range s.vlessBindings {
		if record.Binding.ForwardingRuleID == ruleID && s.vlessBindingOwnedByAdministratorLocked(record.Binding, administratorID) {
			return cloneVLESSCredentialRecord(record), nil
		}
	}
	return vlessidentity.CredentialRecord{}, faults.ErrNotFound
}

func (s *Store) Rotate(_ context.Context, input vlessidentity.RotateInput, event audit.Event) (vlessidentity.Binding, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := vlessMutationKey(input.UpdatedBy, "rotate", input.IdempotencyKey)
	var replay vlessidentity.Binding
	if replayed, err := s.replayBusinessLocked(key, input.RequestSHA256, &replay); err != nil || replayed {
		return replay, replayed, err
	}
	if input.UpdatedBy == "" || input.IdempotencyKey == "" || input.RequestSHA256 == "" || input.UpdatedAt.IsZero() {
		return vlessidentity.Binding{}, false, fmt.Errorf("%w: invalid VLESS rotate mutation", faults.ErrValidation)
	}
	record, exists := s.vlessBindings[input.BindingID]
	if !exists {
		return vlessidentity.Binding{}, false, faults.ErrNotFound
	}
	if !s.vlessBindingOwnedByAdministratorLocked(record.Binding, input.UpdatedBy) {
		return vlessidentity.Binding{}, false, faults.ErrNotFound
	}
	if record.Binding.Revision != input.ExpectedRevision || input.ExpectedRevision < 1 {
		return vlessidentity.Binding{}, false, fmt.Errorf("%w: VLESS binding changed; reload before rotating", faults.ErrConflict)
	}
	if record.Binding.State != vlessidentity.StateActive {
		return vlessidentity.Binding{}, false, fmt.Errorf("%w: revoked VLESS binding cannot be rotated", faults.ErrConflict)
	}
	if input.CredentialUUID == "" || input.CredentialUUID == record.CredentialUUID {
		return vlessidentity.Binding{}, false, fmt.Errorf("%w: replacement VLESS identity is invalid", faults.ErrValidation)
	}
	record.CredentialUUID = input.CredentialUUID
	record.Binding.Revision++
	record.Binding.UpdatedAt = input.UpdatedAt.UTC()
	if err := s.validateVLESSCredentialRecordLocked(record); err != nil {
		return vlessidentity.Binding{}, false, err
	}
	return s.saveVLESSIdentityChangeLocked(key, input.RequestSHA256, record, input.UpdatedBy, "identity_rotated", event)
}

func (s *Store) Revoke(_ context.Context, input vlessidentity.RevokeInput, event audit.Event) (vlessidentity.Binding, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := vlessMutationKey(input.UpdatedBy, "revoke", input.IdempotencyKey)
	var replay vlessidentity.Binding
	if replayed, err := s.replayBusinessLocked(key, input.RequestSHA256, &replay); err != nil || replayed {
		return replay, replayed, err
	}
	if input.UpdatedBy == "" || input.IdempotencyKey == "" || input.RequestSHA256 == "" || input.UpdatedAt.IsZero() {
		return vlessidentity.Binding{}, false, fmt.Errorf("%w: invalid VLESS revoke mutation", faults.ErrValidation)
	}
	record, exists := s.vlessBindings[input.BindingID]
	if !exists {
		return vlessidentity.Binding{}, false, faults.ErrNotFound
	}
	if !s.vlessBindingOwnedByAdministratorLocked(record.Binding, input.UpdatedBy) {
		return vlessidentity.Binding{}, false, faults.ErrNotFound
	}
	if record.Binding.Revision != input.ExpectedRevision || input.ExpectedRevision < 1 {
		return vlessidentity.Binding{}, false, fmt.Errorf("%w: VLESS binding changed; reload before revoking", faults.ErrConflict)
	}
	if record.Binding.State != vlessidentity.StateActive {
		return vlessidentity.Binding{}, false, fmt.Errorf("%w: VLESS binding is already revoked", faults.ErrConflict)
	}
	revokedAt := input.UpdatedAt.UTC()
	record.Binding.State = vlessidentity.StateRevoked
	record.Binding.Revision++
	record.Binding.UpdatedAt = revokedAt
	record.Binding.RevokedAt = &revokedAt
	record.CredentialUUID = ""
	if err := s.validateVLESSCredentialRecordLocked(record); err != nil {
		return vlessidentity.Binding{}, false, err
	}
	return s.saveVLESSIdentityChangeLocked(key, input.RequestSHA256, record, input.UpdatedBy, "identity_revoked", event)
}

func (s *Store) saveVLESSIdentityChangeLocked(key, hash string, record vlessidentity.CredentialRecord, actorID, reason string, event audit.Event) (vlessidentity.Binding, bool, error) {
	public := cloneVLESSBinding(record.Binding)
	if err := s.recordBusinessLocked(key, hash, public.ID, public); err != nil {
		return vlessidentity.Binding{}, false, err
	}
	previous := s.vlessBindings[public.ID]
	previousMaterials := make(map[string]vlessruntime.Material)
	for id, material := range s.vlessRuntimeMaterials {
		if material.BindingID == public.ID && material.State == vlessruntime.StateActive {
			previousMaterials[id] = material
		}
	}
	previousAuditCount := len(s.auditEvents)
	rollback := func() {
		s.vlessBindings[public.ID] = previous
		for id, material := range previousMaterials {
			s.vlessRuntimeMaterials[id] = material
		}
		s.auditEvents = s.auditEvents[:previousAuditCount]
		delete(s.businessIdempotency, key)
	}
	s.vlessBindings[public.ID] = record
	if err := s.revokeRuntimeMaterialsLocked(public.ID, public.UpdatedAt, actorID, reason); err != nil {
		rollback()
		return vlessidentity.Binding{}, false, err
	}
	groupID := s.forwardRules[public.ForwardingRuleID].EntryGroupID
	if err := s.recompileForwardingGroupsLocked([]string{groupID}, public.UpdatedAt); err != nil {
		rollback()
		return vlessidentity.Binding{}, false, err
	}
	s.appendAuditLocked(event)
	return public, false, nil
}

func (s *Store) validateVLESSCredentialRecordLocked(record vlessidentity.CredentialRecord) error {
	binding := record.Binding
	if binding.ID == "" || binding.CustomerID == "" || binding.ForwardingRuleID == "" || binding.EndpointPoolID == "" ||
		!binding.State.Valid() || binding.Revision < 1 || binding.CreatedAt.IsZero() || binding.UpdatedAt.Before(binding.CreatedAt) {
		return fmt.Errorf("%w: invalid VLESS binding metadata", faults.ErrValidation)
	}
	if binding.State == vlessidentity.StateRevoked {
		if binding.RevokedAt == nil || binding.RevokedAt.Before(binding.CreatedAt) || record.CredentialUUID != "" {
			return fmt.Errorf("%w: revoked VLESS binding retained invalid credential state", faults.ErrValidation)
		}
		// Revoked identities are historical audit records. Their rule or pool
		// may be paused, replaced, or retired after revocation; none of those
		// changes may make the snapshot impossible to restore.
		return nil
	}
	_, customerExists := s.customers[binding.CustomerID]
	adminSubject := false
	if rule, exists := s.forwardRules[binding.ForwardingRuleID]; exists && rule.CustomerID == binding.CustomerID {
		adminSubject = s.hasAdministratorSubjectLocked(rule)
	}
	if !customerExists && !adminSubject {
		return fmt.Errorf("%w: customer does not exist", faults.ErrValidation)
	}
	rule, exists := s.forwardRules[binding.ForwardingRuleID]
	if !exists || rule.CustomerID != binding.CustomerID {
		return fmt.Errorf("%w: forwarding rule does not belong to the customer", faults.ErrValidation)
	}
	if rule.EffectiveIngressProtocol() != forwarding.IngressVLESSReality || rule.Protocol != forwarding.ProtocolTCP || rule.EgressMode != forwarding.EgressDirect || rule.IngressReadiness() != forwarding.IngressReady || rule.VLESSFlow != "xtls-rprx-vision" {
		return fmt.Errorf("%w: VLESS identity requires a ready VLESS Reality Vision TCP DIRECT rule", faults.ErrValidation)
	}
	pool, exists := s.endpointPools[binding.EndpointPoolID]
	if !exists || pool.Mode != endpoints.ModeSingleServiceEndpoint || pool.Protocol != "vless" || pool.GroupID != rule.EntryGroupID {
		return fmt.Errorf("%w: endpoint pool is not a matching single-service VLESS pool", faults.ErrValidation)
	}
	switch binding.State {
	case vlessidentity.StateActive:
		if binding.RevokedAt != nil || record.CredentialUUID == "" {
			return fmt.Errorf("%w: active VLESS binding lacks valid credential state", faults.ErrValidation)
		}
		if _, err := provisioningvless.URI(vlessProfile(pool, rule, record.CredentialUUID)); err != nil {
			return fmt.Errorf("%w: invalid VLESS credential or endpoint", faults.ErrValidation)
		}
	default:
		return errors.New("unreachable VLESS binding state")
	}
	return nil
}

func vlessProfile(pool endpoints.EndpointPool, rule forwarding.Rule, credentialUUID string) provisioningvless.Profile {
	destination := rule.RealityDestination
	if destination == "" {
		destination = rule.RealityServerName + ":443"
	}
	return provisioningvless.Profile{
		Endpoint: provisioningvless.Endpoint{Hostname: pool.Hostname, Port: pool.Port, Name: pool.Name},
		Identity: provisioningvless.Identity{UUID: credentialUUID, Flow: rule.VLESSFlow},
		Reality: &provisioningvless.RealityProfile{
			ServerName: rule.RealityServerName, PublicKey: rule.RealityPublicKey,
			ShortID: rule.RealityShortID, Destination: destination, Fingerprint: "chrome",
		},
	}
}

// revokeRuntimeMaterialsLocked invalidates node-local secrets whenever the
// customer identity changes. Existing listeners must not continue accepting
// the previous UUID after a rotate or revoke operation.
func (s *Store) revokeRuntimeMaterialsLocked(bindingID string, at time.Time, actorID, reason string) error {
	type pending struct {
		id    string
		event audit.Event
	}
	pendingEvents := make([]pending, 0)
	for id, material := range s.vlessRuntimeMaterials {
		if material.BindingID != bindingID || material.State != vlessruntime.StateActive {
			continue
		}
		event, err := audit.NewEvent(at.UTC(), "administrator", actorID, "vless_runtime.revoke", "vless_runtime_material", id, "succeeded", map[string]any{
			"binding_id": bindingID, "node_id": material.NodeID, "reason": reason,
		})
		if err != nil {
			return err
		}
		pendingEvents = append(pendingEvents, pending{id: id, event: event})
	}
	for _, item := range pendingEvents {
		material := s.vlessRuntimeMaterials[item.id]
		revokedAt := at.UTC()
		material.State = vlessruntime.StateRevoked
		material.Revision++
		material.UpdatedAt = revokedAt
		material.RevokedAt = &revokedAt
		material.CredentialUUID = ""
		material.RealityPrivateKey = ""
		s.vlessRuntimeMaterials[item.id] = material
		s.appendAuditLocked(item.event)
	}
	return nil
}

func sameVLESSTuple(left, right vlessidentity.Binding) bool {
	return left.CustomerID == right.CustomerID && left.ForwardingRuleID == right.ForwardingRuleID && left.EndpointPoolID == right.EndpointPoolID
}

func (s *Store) vlessBindingOwnedByAdministratorLocked(binding vlessidentity.Binding, administratorID string) bool {
	if administratorID == "" {
		return false
	}
	rule, exists := s.forwardRules[binding.ForwardingRuleID]
	if !exists || !rule.OwnedByAdministrator(administratorID) || rule.CustomerID != binding.CustomerID {
		return false
	}
	pool, exists := s.endpointPools[binding.EndpointPoolID]
	return exists && pool.OwnerID == administratorID && pool.RuleID == binding.ForwardingRuleID
}

func vlessMutationKey(actorID, operation, idempotencyKey string) string {
	return strings.Join([]string{actorID, "vless_identity." + operation, idempotencyKey}, "\x00")
}

func cloneVLESSBinding(binding vlessidentity.Binding) vlessidentity.Binding {
	if binding.RevokedAt != nil {
		value := *binding.RevokedAt
		binding.RevokedAt = &value
	}
	return binding
}

func cloneVLESSCredentialRecord(record vlessidentity.CredentialRecord) vlessidentity.CredentialRecord {
	record.Binding = cloneVLESSBinding(record.Binding)
	return record
}
