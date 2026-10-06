package postgressnapshot

import (
	"context"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/rulegroups"
)

var _ customers.Repository = (*Store)(nil)
var _ forwarding.Repository = (*Store)(nil)
var _ groupconfig.Repository = (*Store)(nil)
var _ rulegroups.Repository = (*Store)(nil)

func (s *Store) ListCustomers(ctx context.Context) ([]customers.Customer, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) ([]customers.Customer, error) { return state.ListCustomers(ctx) })
}

func (s *Store) Customer(ctx context.Context, id string) (customers.Customer, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) (customers.Customer, error) { return state.Customer(ctx, id) })
}

func (s *Store) SaveCustomer(ctx context.Context, input customers.SaveCustomerInput, event audit.Event) (customers.Customer, bool, error) {
	type result struct {
		item     customers.Customer
		replayed bool
	}
	value, err := transact(ctx, s, true, func(state *memoryrepo.Store) (result, error) {
		item, replayed, err := state.SaveCustomer(ctx, input, event)
		return result{item, replayed}, err
	})
	return value.item, value.replayed, err
}

func (s *Store) ListUserGroups(ctx context.Context) ([]customers.UserGroup, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) ([]customers.UserGroup, error) { return state.ListUserGroups(ctx) })
}

func (s *Store) UserGroup(ctx context.Context, id string) (customers.UserGroup, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) (customers.UserGroup, error) { return state.UserGroup(ctx, id) })
}

func (s *Store) SaveUserGroup(ctx context.Context, input customers.SaveUserGroupInput, event audit.Event) (customers.UserGroup, bool, error) {
	type result struct {
		item     customers.UserGroup
		replayed bool
	}
	value, err := transact(ctx, s, true, func(state *memoryrepo.Store) (result, error) {
		item, replayed, err := state.SaveUserGroup(ctx, input, event)
		return result{item, replayed}, err
	})
	return value.item, value.replayed, err
}

func (s *Store) ListGroupNetworks(ctx context.Context) ([]groupconfig.GroupNetwork, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) ([]groupconfig.GroupNetwork, error) { return state.ListGroupNetworks(ctx) })
}

func (s *Store) GroupNetwork(ctx context.Context, id string) (groupconfig.GroupNetwork, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) (groupconfig.GroupNetwork, error) { return state.GroupNetwork(ctx, id) })
}

func (s *Store) UpdateGroupNetwork(ctx context.Context, input groupconfig.UpdateInput, event audit.Event) (groupconfig.GroupNetwork, bool, error) {
	type result struct {
		item     groupconfig.GroupNetwork
		replayed bool
	}
	value, err := transact(ctx, s, true, func(state *memoryrepo.Store) (result, error) {
		item, replayed, err := state.UpdateGroupNetwork(ctx, input, event)
		return result{item, replayed}, err
	})
	return value.item, value.replayed, err
}

func (s *Store) ListForwardingRules(ctx context.Context) ([]forwarding.Rule, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) ([]forwarding.Rule, error) { return state.ListForwardingRules(ctx) })
}

func (s *Store) ForwardingRule(ctx context.Context, id string) (forwarding.Rule, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) (forwarding.Rule, error) { return state.ForwardingRule(ctx, id) })
}

func (s *Store) CreateForwardingRule(ctx context.Context, input forwarding.CreateInput, event audit.Event) (forwarding.Rule, bool, error) {
	type result struct {
		item     forwarding.Rule
		replayed bool
	}
	value, err := transact(ctx, s, true, func(state *memoryrepo.Store) (result, error) {
		item, replayed, err := state.CreateForwardingRule(ctx, input, event)
		return result{item, replayed}, err
	})
	return value.item, value.replayed, err
}

func (s *Store) UpdateForwardingRule(ctx context.Context, input forwarding.UpdateInput, event audit.Event) (forwarding.Rule, bool, error) {
	type result struct {
		item     forwarding.Rule
		replayed bool
	}
	value, err := transact(ctx, s, true, func(state *memoryrepo.Store) (result, error) {
		item, replayed, err := state.UpdateForwardingRule(ctx, input, event)
		return result{item, replayed}, err
	})
	return value.item, value.replayed, err
}

func (s *Store) PreviewForwardingImport(ctx context.Context, input []forwarding.ImportCandidate) ([]forwarding.ImportEvaluation, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) ([]forwarding.ImportEvaluation, error) {
		return state.PreviewForwardingImport(ctx, input)
	})
}

func (s *Store) ImportForwardingRules(ctx context.Context, input forwarding.ImportInput, event audit.Event) (forwarding.ImportResult, bool, error) {
	type result struct {
		item     forwarding.ImportResult
		replayed bool
	}
	value, err := transact(ctx, s, true, func(state *memoryrepo.Store) (result, error) {
		item, replayed, err := state.ImportForwardingRules(ctx, input, event)
		return result{item, replayed}, err
	})
	return value.item, value.replayed, err
}

func (s *Store) ListRuleGroups(ctx context.Context) ([]rulegroups.RuleGroup, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) ([]rulegroups.RuleGroup, error) { return state.ListRuleGroups(ctx) })
}

func (s *Store) ListRuleGroupsForAdministrator(ctx context.Context, adminID string) ([]rulegroups.RuleGroup, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) ([]rulegroups.RuleGroup, error) {
		return state.ListRuleGroupsForAdministrator(ctx, adminID)
	})
}

func (s *Store) RuleGroup(ctx context.Context, id string) (rulegroups.RuleGroup, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) (rulegroups.RuleGroup, error) { return state.RuleGroup(ctx, id) })
}

func (s *Store) RuleGroupForAdministrator(ctx context.Context, adminID, id string) (rulegroups.RuleGroup, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) (rulegroups.RuleGroup, error) {
		return state.RuleGroupForAdministrator(ctx, adminID, id)
	})
}

func (s *Store) SaveRuleGroup(ctx context.Context, input rulegroups.SaveInput, event audit.Event) (rulegroups.RuleGroup, bool, error) {
	type result struct {
		item     rulegroups.RuleGroup
		replayed bool
	}
	value, err := transact(ctx, s, true, func(state *memoryrepo.Store) (result, error) {
		item, replayed, err := state.SaveRuleGroup(ctx, input, event)
		return result{item, replayed}, err
	})
	return value.item, value.replayed, err
}

func (s *Store) BatchUpdateRules(ctx context.Context, input rulegroups.BatchInput, event audit.Event) (rulegroups.BatchResult, bool, error) {
	type result struct {
		item     rulegroups.BatchResult
		replayed bool
	}
	value, err := transact(ctx, s, true, func(state *memoryrepo.Store) (result, error) {
		item, replayed, err := state.BatchUpdateRules(ctx, input, event)
		return result{item, replayed}, err
	})
	return value.item, value.replayed, err
}
