package memoryrepo

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
)

var _ vlessruntime.Repository = (*Store)(nil)

func (s *Store) SaveRuntimeMaterial(_ context.Context, input vlessruntime.SaveInput, event audit.Event) (vlessruntime.PublicMaterial, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := vlessRuntimeMutationKey(input.CreatedBy, "bind", input.IdempotencyKey)
	var replay vlessruntime.PublicMaterial
	if replayed, err := s.replayBusinessLocked(key, input.RequestSHA256, &replay); err != nil || replayed {
		return replay, replayed, err
	}
	if !validRuntimeMutation(input.ID, input.BindingID, input.NodeID, input.CreatedBy, input.IdempotencyKey, input.RequestSHA256) ||
		input.ExpectedRevision != 0 || input.CreatedAt.IsZero() || !vlessruntime.ValidRealityPrivateKey(input.RealityPrivateKey) {
		return vlessruntime.PublicMaterial{}, false, fmt.Errorf("%w: invalid VLESS runtime material mutation", faults.ErrValidation)
	}
	binding, exists := s.vlessBindings[input.BindingID]
	if !exists || binding.Binding.State != vlessidentity.StateActive {
		return vlessruntime.PublicMaterial{}, false, faults.ErrNotFound
	}
	if !s.vlessBindingOwnedByAdministratorLocked(binding.Binding, input.CreatedBy) {
		return vlessruntime.PublicMaterial{}, false, faults.ErrNotFound
	}
	if err := s.validateVLESSCredentialRecordLocked(binding); err != nil {
		return vlessruntime.PublicMaterial{}, false, faults.ErrConflict
	}
	if _, exists := s.vlessRuntimeMaterials[input.ID]; exists {
		return vlessruntime.PublicMaterial{}, false, faults.ErrConflict
	}
	for _, existing := range s.vlessRuntimeMaterials {
		if existing.State == vlessruntime.StateActive && existing.BindingID == input.BindingID && existing.NodeID == input.NodeID {
			return vlessruntime.PublicMaterial{}, false, fmt.Errorf("%w: node is already bound to this VLESS identity", faults.ErrConflict)
		}
	}
	record := vlessruntime.Material{
		ID: input.ID, BindingID: input.BindingID, NodeID: input.NodeID,
		CustomerID: binding.Binding.CustomerID, ForwardingRuleID: binding.Binding.ForwardingRuleID,
		EndpointPoolID: binding.Binding.EndpointPoolID, State: vlessruntime.StateActive,
		Revision: 1, CreatedAt: input.CreatedAt.UTC(), UpdatedAt: input.CreatedAt.UTC(),
		CredentialUUID: binding.CredentialUUID, RealityPrivateKey: strings.TrimSpace(input.RealityPrivateKey),
	}
	if err := s.validateVLESSRuntimeMaterialLocked(record); err != nil {
		return vlessruntime.PublicMaterial{}, false, err
	}
	public := record.Public()
	if err := s.recordBusinessLocked(key, input.RequestSHA256, public.ID, public); err != nil {
		return vlessruntime.PublicMaterial{}, false, err
	}
	s.vlessRuntimeMaterials[record.ID] = record
	groupID := s.forwardRules[record.ForwardingRuleID].EntryGroupID
	if err := s.recompileForwardingGroupsLocked([]string{groupID}, record.UpdatedAt); err != nil {
		delete(s.vlessRuntimeMaterials, record.ID)
		delete(s.businessIdempotency, key)
		return vlessruntime.PublicMaterial{}, false, err
	}
	s.appendAuditLocked(s.safeRuntimeAuditEvent(event, "vless_runtime.bind", input.CreatedBy, public, false))
	return public, false, nil
}

func (s *Store) ListPublicRuntimeMaterials(_ context.Context) ([]vlessruntime.PublicMaterial, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]vlessruntime.PublicMaterial, 0, len(s.vlessRuntimeMaterials))
	for _, material := range s.vlessRuntimeMaterials {
		items = append(items, material.Public())
	}
	sort.Slice(items, func(left, right int) bool {
		if items[left].CreatedAt.Equal(items[right].CreatedAt) {
			return items[left].ID < items[right].ID
		}
		return items[left].CreatedAt.Before(items[right].CreatedAt)
	})
	return items, nil
}

func (s *Store) ListPublicRuntimeMaterialsForAdministrator(_ context.Context, administratorID string) ([]vlessruntime.PublicMaterial, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]vlessruntime.PublicMaterial, 0)
	for _, material := range s.vlessRuntimeMaterials {
		binding, exists := s.vlessBindings[material.BindingID]
		if exists && s.vlessBindingOwnedByAdministratorLocked(binding.Binding, administratorID) {
			items = append(items, material.Public())
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

func (s *Store) RuntimeMaterialForNode(_ context.Context, nodeID, bindingID string) (vlessruntime.Material, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.runtimeMaterialForNodeLocked(strings.TrimSpace(nodeID), strings.TrimSpace(bindingID))
}

// runtimeMaterialForNodeLocked is the only lock-held secret read used by the
// node bundle compiler. It never returns a material for a different node,
// binding, revoked record, or invalid authoritative relationship.
func (s *Store) runtimeMaterialForNodeLocked(nodeID, bindingID string) (vlessruntime.Material, error) {
	if nodeID == "" || bindingID == "" {
		return vlessruntime.Material{}, faults.ErrNotFound
	}
	hasRevokedMaterial := false
	for _, material := range s.vlessRuntimeMaterials {
		if material.NodeID != nodeID || material.BindingID != bindingID {
			continue
		}
		if material.State != vlessruntime.StateActive {
			hasRevokedMaterial = true
			continue
		}
		if err := s.validateVLESSRuntimeMaterialLocked(material); err != nil {
			return vlessruntime.Material{}, errors.New("invalid persisted VLESS runtime material")
		}
		return cloneVLESSRuntimeMaterial(material), nil
	}
	if hasRevokedMaterial {
		return vlessruntime.Material{}, faults.ErrNotFound
	}
	binding, exists := s.vlessBindings[bindingID]
	if !exists || binding.Binding.State != vlessidentity.StateActive || s.validateVLESSCredentialRecordLocked(binding) != nil {
		return vlessruntime.Material{}, faults.ErrNotFound
	}
	rule, exists := s.forwardRules[binding.Binding.ForwardingRuleID]
	if !exists || rule.CustomerID != binding.Binding.CustomerID || rule.IngressReadiness() != forwarding.IngressReady {
		return vlessruntime.Material{}, faults.ErrNotFound
	}
	privateKey := s.autoRealityKeys[rule.ID]
	if privateKey == "" || !vlessruntime.RealityKeyMatchesPublicKey(privateKey, rule.RealityPublicKey) {
		return vlessruntime.Material{}, faults.ErrNotFound
	}
	pool := s.endpointPools[binding.Binding.EndpointPoolID]
	member, memberExists := s.membersByGroup[rule.EntryGroupID][nodeID]
	endpointMember, endpointExists := s.endpointMembers[pool.ID][nodeID]
	if _, nodeExists := s.nodes[nodeID]; !nodeExists || !memberExists || member.RetiredAt != nil || !endpointExists || endpointMember.GroupID != rule.EntryGroupID || endpointMember.NodeID != nodeID {
		return vlessruntime.Material{}, faults.ErrNotFound
	}
	return vlessruntime.Material{
		BindingID: bindingID, NodeID: nodeID, CustomerID: rule.CustomerID,
		ForwardingRuleID: rule.ID, EndpointPoolID: pool.ID,
		CredentialUUID: binding.CredentialUUID, RealityPrivateKey: privateKey,
	}, nil
}

func (s *Store) RevokeRuntimeMaterial(_ context.Context, input vlessruntime.RevokeInput, event audit.Event) (vlessruntime.PublicMaterial, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := vlessRuntimeMutationKey(input.UpdatedBy, "revoke", input.IdempotencyKey)
	var replay vlessruntime.PublicMaterial
	if replayed, err := s.replayBusinessLocked(key, input.RequestSHA256, &replay); err != nil || replayed {
		return replay, replayed, err
	}
	if !validRuntimeMutation(input.ID, input.UpdatedBy, input.IdempotencyKey, input.RequestSHA256) || input.ExpectedRevision < 1 || input.UpdatedAt.IsZero() {
		return vlessruntime.PublicMaterial{}, false, fmt.Errorf("%w: invalid VLESS runtime revoke mutation", faults.ErrValidation)
	}
	record, exists := s.vlessRuntimeMaterials[input.ID]
	if !exists {
		return vlessruntime.PublicMaterial{}, false, faults.ErrNotFound
	}
	binding, bindingExists := s.vlessBindings[record.BindingID]
	if !bindingExists || !s.vlessBindingOwnedByAdministratorLocked(binding.Binding, input.UpdatedBy) {
		return vlessruntime.PublicMaterial{}, false, faults.ErrNotFound
	}
	if record.Revision != input.ExpectedRevision || record.State != vlessruntime.StateActive {
		return vlessruntime.PublicMaterial{}, false, fmt.Errorf("%w: VLESS runtime material changed or is already revoked", faults.ErrConflict)
	}
	previous := record
	revokedAt := input.UpdatedAt.UTC()
	record.State = vlessruntime.StateRevoked
	record.Revision++
	record.UpdatedAt = revokedAt
	record.RevokedAt = &revokedAt
	record.CredentialUUID = ""
	record.RealityPrivateKey = ""
	if err := s.validateVLESSRuntimeMaterialLocked(record); err != nil {
		return vlessruntime.PublicMaterial{}, false, err
	}
	public := record.Public()
	if err := s.recordBusinessLocked(key, input.RequestSHA256, public.ID, public); err != nil {
		return vlessruntime.PublicMaterial{}, false, err
	}
	s.vlessRuntimeMaterials[record.ID] = record
	groupID := s.forwardRules[record.ForwardingRuleID].EntryGroupID
	if err := s.recompileForwardingGroupsLocked([]string{groupID}, record.UpdatedAt); err != nil {
		s.vlessRuntimeMaterials[record.ID] = previous
		delete(s.businessIdempotency, key)
		return vlessruntime.PublicMaterial{}, false, err
	}
	s.appendAuditLocked(s.safeRuntimeAuditEvent(event, "vless_runtime.revoke", input.UpdatedBy, public, true))
	return public, false, nil
}

// validateVLESSRuntimeMaterialLocked checks every relationship needed before
// a node can receive a secret. It intentionally returns generic errors so
// callers cannot use validation failures as a secret-bearing lookup oracle.
func (s *Store) validateVLESSRuntimeMaterialLocked(material vlessruntime.Material) error {
	if material.ID == "" || material.BindingID == "" || material.NodeID == "" || material.CustomerID == "" ||
		material.ForwardingRuleID == "" || material.EndpointPoolID == "" || !material.State.Valid() || material.Revision < 1 ||
		material.CreatedAt.IsZero() || material.UpdatedAt.Before(material.CreatedAt) {
		return fmt.Errorf("%w: invalid VLESS runtime material metadata", faults.ErrValidation)
	}
	if material.State == vlessruntime.StateRevoked {
		if material.RevokedAt == nil || material.RevokedAt.Before(material.CreatedAt) || material.CredentialUUID != "" || material.RealityPrivateKey != "" {
			return fmt.Errorf("%w: revoked VLESS runtime material retained secret state", faults.ErrValidation)
		}
		// Revoked records are historical audit state. They remain readable in a
		// snapshot even after an identity, rule, pool, or node has been retired.
		// If the binding still exists, its immutable references must agree.
		if binding, exists := s.vlessBindings[material.BindingID]; exists {
			if binding.Binding.ID == "" || binding.Binding.CustomerID != material.CustomerID ||
				binding.Binding.ForwardingRuleID != material.ForwardingRuleID || binding.Binding.EndpointPoolID != material.EndpointPoolID {
				return fmt.Errorf("%w: revoked VLESS runtime identity references changed binding", faults.ErrValidation)
			}
		}
		return nil
	}
	binding, exists := s.vlessBindings[material.BindingID]
	if !exists || binding.Binding.ID == "" || binding.Binding.State != vlessidentity.StateActive ||
		binding.Binding.CustomerID != material.CustomerID || binding.Binding.ForwardingRuleID != material.ForwardingRuleID ||
		binding.Binding.EndpointPoolID != material.EndpointPoolID || !vlessruntime.ValidCredentialUUID(binding.CredentialUUID) ||
		material.CredentialUUID != binding.CredentialUUID {
		return fmt.Errorf("%w: invalid VLESS runtime identity binding", faults.ErrValidation)
	}
	rule, exists := s.forwardRules[material.ForwardingRuleID]
	if !exists || rule.CustomerID != material.CustomerID || rule.EffectiveIngressProtocol() != forwarding.IngressVLESSReality ||
		rule.Protocol != forwarding.ProtocolTCP || rule.EgressMode != forwarding.EgressDirect || rule.IngressReadiness() != forwarding.IngressReady ||
		rule.RealityPublicKey == "" || !vlessruntime.RealityKeyMatchesPublicKey(material.RealityPrivateKey, rule.RealityPublicKey) {
		return fmt.Errorf("%w: runtime material does not match the ready VLESS Reality rule", faults.ErrConflict)
	}
	pool, exists := s.endpointPools[material.EndpointPoolID]
	if !exists || pool.Mode != endpoints.ModeSingleServiceEndpoint || pool.Protocol != "vless" || pool.GroupID != rule.EntryGroupID {
		return fmt.Errorf("%w: invalid VLESS runtime endpoint pool", faults.ErrConflict)
	}
	if _, exists := s.nodes[material.NodeID]; !exists {
		return fmt.Errorf("%w: VLESS runtime node does not exist", faults.ErrConflict)
	}
	member, exists := s.membersByGroup[pool.GroupID][material.NodeID]
	if !exists || member.RetiredAt != nil {
		return fmt.Errorf("%w: VLESS runtime node is not an active entry-group member", faults.ErrConflict)
	}
	endpointMember, exists := s.endpointMembers[pool.ID][material.NodeID]
	if !exists || endpointMember.GroupID != pool.GroupID || endpointMember.NodeID != material.NodeID {
		return fmt.Errorf("%w: VLESS runtime node is not an endpoint candidate", faults.ErrConflict)
	}
	if material.RevokedAt != nil || !vlessruntime.ValidCredentialUUID(material.CredentialUUID) || !vlessruntime.ValidRealityPrivateKey(material.RealityPrivateKey) {
		return fmt.Errorf("%w: active VLESS runtime material lacks valid secrets", faults.ErrValidation)
	}
	return nil
}

func (s *Store) safeRuntimeAuditEvent(event audit.Event, action, actorID string, public vlessruntime.PublicMaterial, revoked bool) audit.Event {
	event.ActorType = "administrator"
	event.ActorID = actorID
	event.Action = action
	event.ResourceType = "vless_runtime_material"
	event.ResourceID = public.ID
	event.Metadata = map[string]any{
		"binding_id": public.BindingID,
		"node_id":    public.NodeID,
		"revision":   public.Revision,
		"state":      public.State,
	}
	if revoked {
		event.Metadata["revoked"] = true
	}
	return event
}

func validRuntimeMutation(values ...string) bool {
	if len(values) == 0 {
		return false
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return false
		}
	}
	// The last value is the request digest for both mutation shapes.
	digestValue := values[len(values)-1]
	if len(digestValue) != 64 || digestValue != strings.ToLower(digestValue) {
		return false
	}
	_, err := hex.DecodeString(digestValue)
	return err == nil
}

func vlessRuntimeMutationKey(actorID, operation, idempotencyKey string) string {
	return strings.Join([]string{actorID, "vless_runtime." + operation, idempotencyKey}, "\x00")
}

func cloneVLESSRuntimeMaterial(material vlessruntime.Material) vlessruntime.Material {
	if material.RevokedAt != nil {
		value := *material.RevokedAt
		material.RevokedAt = &value
	}
	return material
}
