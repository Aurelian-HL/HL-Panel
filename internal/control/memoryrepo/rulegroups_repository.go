package memoryrepo

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/rulegroups"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
	provisioningvless "github.com/hongle/hl-panel/internal/provisioning/vless"
)

var _ rulegroups.Repository = (*Store)(nil)

func (s *Store) ListRuleGroups(_ context.Context) ([]rulegroups.RuleGroup, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]rulegroups.RuleGroup, 0, len(s.ruleGroups))
	for _, item := range s.ruleGroups {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Name == items[j].Name {
			return items[i].ID < items[j].ID
		}
		return items[i].Name < items[j].Name
	})
	return items, nil
}

func (s *Store) ListRuleGroupsForAdministrator(_ context.Context, adminID string) ([]rulegroups.RuleGroup, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]rulegroups.RuleGroup, 0)
	for _, item := range s.ruleGroups {
		if adminID != "" && item.OwnerAdministratorID == adminID {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Name == items[j].Name {
			return items[i].ID < items[j].ID
		}
		return items[i].Name < items[j].Name
	})
	return items, nil
}

func (s *Store) RuleGroup(_ context.Context, id string) (rulegroups.RuleGroup, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, exists := s.ruleGroups[id]
	if !exists {
		return rulegroups.RuleGroup{}, faults.ErrNotFound
	}
	return item, nil
}

func (s *Store) RuleGroupForAdministrator(_ context.Context, adminID, id string) (rulegroups.RuleGroup, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, exists := s.ruleGroups[id]
	if !exists || adminID == "" || item.OwnerAdministratorID != adminID {
		return rulegroups.RuleGroup{}, faults.ErrNotFound
	}
	return item, nil
}

func (s *Store) SaveRuleGroup(_ context.Context, input rulegroups.SaveInput, event audit.Event) (rulegroups.RuleGroup, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if input.IdempotencyKey == "" || input.RequestSHA256 == "" || input.CreatedBy == "" {
		return rulegroups.RuleGroup{}, false, fmt.Errorf("%w: mutation actor, idempotency key and request hash are required", faults.ErrValidation)
	}
	operation := "rule_group.create"
	if input.ExpectedRevision > 0 {
		operation = "rule_group.update"
	}
	key := input.CreatedBy + "\x00" + operation + "\x00" + input.IdempotencyKey
	var replay rulegroups.RuleGroup
	if ok, err := s.replayBusinessLocked(key, input.RequestSHA256, &replay); ok || err != nil {
		return replay, ok, err
	}
	item := input.RuleGroup
	if item.OwnerAdministratorID == "" || item.OwnerAdministratorID != input.CreatedBy {
		return rulegroups.RuleGroup{}, false, faults.ErrNotFound
	}
	previous, exists := s.ruleGroups[item.ID]
	if exists && previous.OwnerAdministratorID != input.CreatedBy {
		return rulegroups.RuleGroup{}, false, faults.ErrNotFound
	}
	if input.ExpectedRevision < 0 || input.ExpectedRevision == math.MaxInt64 || item.Revision != input.ExpectedRevision+1 || exists && input.ExpectedRevision == 0 || !exists && input.ExpectedRevision > 0 || exists && previous.Revision != input.ExpectedRevision {
		return rulegroups.RuleGroup{}, false, fmt.Errorf("%w: rule group changed; reload before saving", faults.ErrConflict)
	}
	for id, other := range s.ruleGroups {
		if id != item.ID && other.OwnerAdministratorID == item.OwnerAdministratorID && strings.EqualFold(other.Name, item.Name) {
			return rulegroups.RuleGroup{}, false, fmt.Errorf("%w: rule group name already exists", faults.ErrConflict)
		}
	}
	if exists {
		item.CreatedAt = previous.CreatedAt
	}
	if _, err := rulegroups.NormalizeRequest(rulegroups.Request{Name: item.Name, Description: item.Description, Revision: input.ExpectedRevision}); err != nil {
		return rulegroups.RuleGroup{}, false, err
	}
	if err := s.recordBusinessLocked(key, input.RequestSHA256, item.ID, item); err != nil {
		return rulegroups.RuleGroup{}, false, err
	}
	s.ruleGroups[item.ID] = item
	s.appendAuditLocked(event)
	return item, false, nil
}

func (s *Store) BatchUpdateRules(_ context.Context, input rulegroups.BatchInput, event audit.Event) (rulegroups.BatchResult, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := input.CreatedBy + "\x00forwarding.batch\x00" + input.IdempotencyKey
	var replay rulegroups.BatchResult
	if ok, err := s.replayBusinessLocked(key, input.RequestSHA256, &replay); ok || err != nil {
		if ok {
			for i := range replay.Items {
				replay.Items[i] = s.forwardingViewLocked(replay.Items[i])
			}
		}
		return replay, ok, err
	}
	request := input.Request
	if request.Operation == rulegroups.BatchMoveGroup && request.RuleGroupID != "" {
		group, exists := s.ruleGroups[request.RuleGroupID]
		adminID, adminScoped := strings.CutPrefix(input.CreatedBy, "admin-rules:")
		if !exists || adminScoped && group.OwnerAdministratorID != adminID || !adminScoped && group.OwnerAdministratorID != input.CreatedBy {
			return rulegroups.BatchResult{}, false, faults.ErrNotFound
		}
	}
	items := make([]forwarding.Rule, 0, len(request.RuleIDs))
	previous := make(map[string]forwarding.Rule, len(request.RuleIDs))
	endpointIDs := make([]string, 0, len(request.RuleIDs))
	removedEndpoints := make(map[string]endpoints.EndpointPool)
	removedMembers := make(map[string]map[string]endpoints.EndpointPoolMember)
	removedSOCKS5Upstreams := make(map[string]provisioningvless.SOCKS5Upstream)
	removedAutoRealityKeys := make(map[string]string)
	removedVLESSBindings := make(map[string]vlessidentity.CredentialRecord)
	revokedVLESSBindings := make(map[string]vlessidentity.CredentialRecord)
	revokedVLESSAudits := make([]audit.Event, 0)
	groups := make([]string, 0, len(request.RuleIDs))
	for _, id := range request.RuleIDs {
		current, exists := s.forwardRules[id]
		if !exists {
			return rulegroups.BatchResult{}, false, fmt.Errorf("%w: forwarding rule %s does not exist", faults.ErrNotFound, id)
		}
		if input.ExpectedCustomerID != "" && current.CustomerID != input.ExpectedCustomerID {
			return rulegroups.BatchResult{}, false, faults.ErrNotFound
		}
		adminID, adminScoped := strings.CutPrefix(input.CreatedBy, "admin-rules:")
		if adminScoped && !current.OwnedByAdministrator(adminID) || !adminScoped && current.OwnerKind == forwarding.OwnerAdministrator && !current.OwnedByAdministrator(input.CreatedBy) {
			return rulegroups.BatchResult{}, false, faults.ErrNotFound
		}
		if current.Revision != request.ExpectedRevisions[id] || current.Revision == math.MaxInt64 {
			return rulegroups.BatchResult{}, false, fmt.Errorf("%w: forwarding rule %s changed; reload before saving", faults.ErrConflict, id)
		}
		previous[id] = current
		groups = append(groups, current.EntryGroupID)
		item := cloneForwardingRule(current)
		switch request.Operation {
		case rulegroups.BatchPause:
			item.Paused = true
		case rulegroups.BatchResume:
			item.Paused = false
			customer := s.customers[item.CustomerID]
			if current.OwnerKind != forwarding.OwnerAdministrator && customer.EffectiveStatus(input.UpdatedAt) != customers.StatusActive {
				return rulegroups.BatchResult{}, false, fmt.Errorf("%w: forwarding rule %s customer is not active", faults.ErrConflict, id)
			}
		case rulegroups.BatchMoveGroup:
			item.RuleGroupID = request.RuleGroupID
		case rulegroups.BatchDelete:
			for _, pool := range s.endpointPools {
				if pool.RuleID == item.ID {
					for _, material := range s.vlessRuntimeMaterials {
						if material.EndpointPoolID == pool.ID && material.State == vlessruntime.StateActive {
							return rulegroups.BatchResult{}, false, fmt.Errorf("%w: endpoint pool has active runtime material", faults.ErrConflict)
						}
					}
					for _, member := range s.endpointMembers[pool.ID] {
						if member.ActiveConnections > 0 {
							return rulegroups.BatchResult{}, false, fmt.Errorf("%w: endpoint pool has active connections", faults.ErrConflict)
						}
					}
					for bindingID, record := range s.vlessBindings {
						if record.Binding.EndpointPoolID != pool.ID || record.Binding.State != vlessidentity.StateActive {
							continue
						}
						candidate := cloneVLESSCredentialRecord(record)
						revokedAt := input.UpdatedAt.UTC()
						candidate.Binding.State = vlessidentity.StateRevoked
						candidate.Binding.Revision++
						candidate.Binding.UpdatedAt = revokedAt
						candidate.Binding.RevokedAt = &revokedAt
						candidate.CredentialUUID = ""
						if err := s.validateVLESSCredentialRecordLocked(candidate); err != nil {
							return rulegroups.BatchResult{}, false, fmt.Errorf("%w: VLESS identity %s cannot be revoked for rule deletion", faults.ErrConflict, bindingID)
						}
						removedVLESSBindings[bindingID] = record
						revokedVLESSBindings[bindingID] = candidate
						revocationAudit, err := audit.NewEvent(input.UpdatedAt, event.ActorType, event.ActorID, "vless_identity.revoke", "vless_identity", bindingID, "succeeded", map[string]any{
							"forwarding_rule_id": item.ID,
							"endpoint_pool_id":   pool.ID,
							"reason":             "forwarding_rule_deleted",
						})
						if err != nil {
							return rulegroups.BatchResult{}, false, err
						}
						revokedVLESSAudits = append(revokedVLESSAudits, revocationAudit)
					}
					endpointIDs = append(endpointIDs, pool.ID)
				}
			}
			// Preserve the deleted row in the idempotent result. No revision is
			// created because the resource no longer exists after this commit.
			items = append(items, item)
			continue
		}
		item.Revision++
		item.UpdatedAt = input.UpdatedAt
		item.Deployed = false
		item.Status = forwarding.StatusPendingActivation
		items = append(items, item)
	}
	result := rulegroups.BatchResult{Operation: request.Operation, RuleGroupID: request.RuleGroupID, Items: make([]forwarding.Rule, len(items))}
	for i, item := range items {
		result.Items[i] = s.forwardingViewLocked(item)
	}
	if err := s.recordBusinessLocked(key, input.RequestSHA256, "batch", result); err != nil {
		return rulegroups.BatchResult{}, false, err
	}
	if request.Operation == rulegroups.BatchDelete {
		// Active VLESS identities are revoked as part of deleting their owning
		// forwarding rules. Persist the historical state once before removing
		// the rules and endpoint pools below.
		for bindingID, binding := range revokedVLESSBindings {
			s.vlessBindings[bindingID] = binding
		}
	}
	for _, item := range items {
		if request.Operation == rulegroups.BatchDelete {
			delete(s.forwardRules, item.ID)
			if privateKey, ok := s.autoRealityKeys[item.ID]; ok {
				removedAutoRealityKeys[item.ID] = privateKey
			}
			delete(s.autoRealityKeys, item.ID)
			if upstream, ok := s.vlessSOCKS5UpstreamLocked(item.ID); ok {
				removedSOCKS5Upstreams[item.ID] = upstream
			}
			s.deleteVLESSSOCKS5UpstreamLocked(item.ID)
		} else {
			s.forwardRules[item.ID] = cloneForwardingRule(item)
		}
	}
	for _, id := range endpointIDs {
		removedEndpoints[id] = s.endpointPools[id]
		removedMembers[id] = s.endpointMembers[id]
		delete(s.endpointPools, id)
		delete(s.endpointMembers, id)
	}
	if err := s.recompileForwardingGroupsLocked(groups, event.CreatedAt); err != nil {
		for id, item := range previous {
			s.forwardRules[id] = item
		}
		// A failed compilation must not leave endpoint state partially removed.
		for id, pool := range removedEndpoints {
			s.endpointPools[id] = pool
			s.endpointMembers[id] = removedMembers[id]
		}
		for id, upstream := range removedSOCKS5Upstreams {
			s.vlessSOCKS5Upstreams[id] = upstream
		}
		for id, privateKey := range removedAutoRealityKeys {
			s.autoRealityKeys[id] = privateKey
		}
		for id, binding := range removedVLESSBindings {
			s.vlessBindings[id] = binding
		}
		delete(s.businessIdempotency, key)
		return rulegroups.BatchResult{}, false, err
	}
	for _, revocationAudit := range revokedVLESSAudits {
		s.appendAuditLocked(revocationAudit)
	}
	s.appendAuditLocked(event)
	return result, false, nil
}
