package memoryrepo

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
)

// The import actor manages the imported listener without becoming its owner.
// All three persisted relationships must agree; a native flag alone grants nothing.
func (s *Store) importedRuleManagedByLocked(rule forwarding.Rule, adminID string) bool {
	if s.forwardingProjection != nil {
		return s.forwardingProjection.imported[rule.ID][adminID]
	}
	baseline := rule.ImportedTraffic
	if adminID == "" || rule.VLESSOutboundMode != forwarding.VLESSOutboundNative || baseline.SourceID == "" || !baseline.Valid() || (rule.OwnerKind != forwarding.OwnerCustomer && !rule.IsUnassignedImport() && !(rule.OwnerKind == forwarding.OwnerAdministrator && rule.OwnerID == adminID)) {
		return false
	}
	for _, pool := range s.endpointPools {
		if pool.RuleID != rule.ID || pool.OwnerID != adminID || pool.GroupID != rule.EntryGroupID || pool.Port != rule.ListenPort {
			continue
		}
		for _, record := range s.vlessBindings {
			b := record.Binding
			if b.ForwardingRuleID == rule.ID && b.CustomerID == rule.CustomerID && b.EndpointPoolID == pool.ID && b.ImportedFrom != nil && b.ImportedFrom.BatchID == baseline.BatchID && b.ImportedFrom.LineID == baseline.LineID {
				return true
			}
		}
	}
	return false
}

// Administrator-authored create records prove delegated management. Preserve
// the original actor namespace so retries from earlier releases remain idempotent.
// Customer-authored rules use a distinct customer: namespace and grant nothing.
func (s *Store) delegatedRuleManagedByLocked(rule forwarding.Rule, adminID string) bool {
	if s.forwardingProjection != nil {
		return s.forwardingProjection.delegated[rule.ID][adminID]
	}
	if adminID == "" || !rule.OwnedByCustomer(rule.CustomerID) {
		return false
	}
	prefix := adminID + "\x00forwarding.create\x00"
	for key, mutation := range s.businessIdempotency {
		if mutation.ResourceID != rule.ID || !strings.HasPrefix(key, prefix) {
			continue
		}
		var created forwarding.Rule
		if json.Unmarshal(mutation.Result, &created) == nil && created.OwnerKind == forwarding.OwnerCustomer && created.OwnerID == rule.CustomerID && created.CustomerID == rule.CustomerID && created.ID == rule.ID {
			return true
		}
	}
	return false
}

// ManagedForwardingRule uses exactly the list's ownership/provenance checks,
// but evaluates readiness only for the requested rule.
func (s *Store) ManagedForwardingRule(_ context.Context, adminID, id string) (forwarding.Rule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rule, exists := s.forwardRules[id]
	if !exists || (!rule.OwnedByAdministrator(adminID) && !s.importedRuleManagedByLocked(rule, adminID) && !s.delegatedRuleManagedByLocked(rule, adminID)) {
		return forwarding.Rule{}, faults.ErrNotFound
	}
	return s.forwardingViewLocked(rule), nil
}

func (s *Store) ListManagedForwardingRules(_ context.Context, adminID string) ([]forwarding.Rule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]forwarding.Rule, 0)
	for _, rule := range s.forwardRules {
		if rule.OwnedByAdministrator(adminID) || s.importedRuleManagedByLocked(rule, adminID) || s.delegatedRuleManagedByLocked(rule, adminID) {
			items = append(items, s.forwardingViewLocked(rule))
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	return items, nil
}

func (s *Store) importedSubscriptionSubjectLocked(ruleID, subject, actor string) bool {
	r, ok := s.forwardRules[ruleID]
	return ok && r.CustomerID == subject && r.OwnerKind == forwarding.OwnerAdministrator && r.OwnerID == actor && s.importedRuleManagedByLocked(r, actor) && s.hasAdministratorSubjectLocked(r)
}
