package memoryrepo

import (
	"context"
	"sort"

	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// UsageReadView owns immutable accounting inputs. It never retains a mutable
// Store or a transaction-bound history reader. Archived generations are resolved
// by the persistence adapter in the requesting transaction.
type UsageReadView struct {
	unassigned map[string]bool
	customers  map[string]customers.Customer
	rules      map[string]forwarding.RuleTrafficState
	configs    map[string]map[int64]deploymentreceipts.HistoricalInput
}

func (s *Store) UsageReadView() *UsageReadView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v := &UsageReadView{
		unassigned: make(map[string]bool),
		customers:  make(map[string]customers.Customer, len(s.customers)),
		rules:      make(map[string]forwarding.RuleTrafficState, len(s.forwardRules)),
		configs:    make(map[string]map[int64]deploymentreceipts.HistoricalInput, len(s.nodeConfigsByNode)),
	}
	for id, c := range s.customers {
		v.customers[id] = s.customerWithLoginLocked(c)
	}
	importedAdministrator := false
	for id, r := range s.forwardRules {
		v.rules[id] = forwarding.TrafficState(r)
		if r.IsUnassignedImport() {
			v.unassigned[r.CustomerID] = true
		} else if r.OwnerKind == forwarding.OwnerAdministrator && r.OwnerID != "" &&
			r.VLESSOutboundMode == forwarding.VLESSOutboundNative && r.ImportedTraffic.SourceID != "" && len(r.CustomerID) <= 128 {
			importedAdministrator = true
		}
	}
	if importedAdministrator {
		s.addImportedAdministratorUsageSubjectsLocked(v.unassigned)
	}
	for nodeID, configs := range s.nodeConfigsByNode {
		v.configs[nodeID] = make(map[int64]deploymentreceipts.HistoricalInput, len(configs))
		for generation, c := range configs {
			h := NodeConfigHistory{c, s.applyResultsByNode[nodeID][generation], s.applyAttemptsByNode[nodeID][generation]}
			v.configs[nodeID][generation] = h.UsageInput()
		}
	}
	return v
}

// Keep the temporary administrator index out of the ordinary constructor's
// stack frame; unassigned and customer-owned views need no import index.
func (s *Store) addImportedAdministratorUsageSubjectsLocked(subjects map[string]bool) {
	admins := make(map[string]bool, len(s.adminsByUsername))
	for _, admin := range s.adminsByUsername {
		admins[admin.ID] = true
	}
	// A validated import already proves the alternate subject accepted by
	// hasAdministratorSubjectLocked. Preserve its admin-existence and length
	// checks without rescanning every pool and binding for every rule.
	s.visitImportedManagementLocked(func(r forwarding.Rule, actor string) {
		if r.OwnerKind == forwarding.OwnerAdministrator && r.OwnerID == actor && admins[actor] && len(r.CustomerID) <= 128 {
			subjects[r.CustomerID] = true
		}
	})
}

// UsageInput detaches historical evidence, including confidential config bytes.
func (h NodeConfigHistory) UsageInput() deploymentreceipts.HistoricalInput {
	input := deploymentreceipts.HistoricalInput{Config: cloneNodeConfig(h.Config),
		AttemptID: h.Attempt.CurrentID, Invalidated: h.Attempt.Invalidated,
		ApplyResults: make(map[agentv1.ApplyPhase]generations.ApplyResult, len(h.Results))}
	for phase, r := range h.Results {
		input.ApplyResults[agentv1.ApplyPhase(phase)] = r
	}
	return input
}

func (v *UsageReadView) Customer(_ context.Context, id string) (customers.Customer, error) {
	c, ok := v.customers[id]
	if !ok {
		return customers.Customer{}, faults.ErrNotFound
	}
	return cloneCustomer(c), nil
}

func (v *UsageReadView) RuleTrafficState(_ context.Context, id string) (forwarding.RuleTrafficState, error) {
	r, ok := v.rules[id]
	if !ok {
		return forwarding.RuleTrafficState{}, faults.ErrNotFound
	}
	return r, nil
}

func (v *UsageReadView) ListRuleTrafficStates(_ context.Context) ([]forwarding.RuleTrafficState, error) {
	items := make([]forwarding.RuleTrafficState, 0, len(v.rules))
	for _, r := range v.rules {
		items = append(items, r)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

func (v *UsageReadView) UsageConfigInput(_ context.Context, nodeID string, generation int64) (deploymentreceipts.HistoricalInput, error) {
	input, ok := v.configs[nodeID][generation]
	if !ok {
		return deploymentreceipts.HistoricalInput{}, faults.ErrNotFound
	}
	input.Config = cloneNodeConfig(input.Config)
	results := make(map[agentv1.ApplyPhase]generations.ApplyResult, len(input.ApplyResults))
	for phase, r := range input.ApplyResults {
		results[phase] = r
	}
	input.ApplyResults = results
	return input, nil
}

func (s *Store) RuleTrafficState(_ context.Context, id string) (forwarding.RuleTrafficState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.forwardRules[id]
	if !ok {
		return forwarding.RuleTrafficState{}, faults.ErrNotFound
	}
	return forwarding.TrafficState(r), nil
}

func (s *Store) ListRuleTrafficStates(_ context.Context) ([]forwarding.RuleTrafficState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]forwarding.RuleTrafficState, 0, len(s.forwardRules))
	for _, r := range s.forwardRules {
		items = append(items, forwarding.TrafficState(r))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

func (v *UsageReadView) ValidateUnassignedSubject(_ context.Context, subject string) error {
	if v.unassigned[subject] {
		return nil
	}
	return faults.ErrNotFound
}
