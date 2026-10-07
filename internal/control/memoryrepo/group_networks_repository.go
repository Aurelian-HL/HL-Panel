package memoryrepo

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/groups"
)

var _ groupconfig.Repository = (*Store)(nil)

func (s *Store) ListGroupNetworks(_ context.Context) ([]groupconfig.GroupNetwork, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]groupconfig.GroupNetwork, 0, len(s.groupNetworks))
	for _, item := range s.groupNetworks {
		items = append(items, cloneGroupNetwork(item))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].GroupID < items[j].GroupID })
	return items, nil
}

func (s *Store) GroupNetwork(_ context.Context, id string) (groupconfig.GroupNetwork, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, found := s.groupNetworks[id]
	if !found {
		return groupconfig.GroupNetwork{}, faults.ErrNotFound
	}
	return cloneGroupNetwork(item), nil
}

func (s *Store) UpdateGroupNetwork(_ context.Context, input groupconfig.UpdateInput, event audit.Event) (groupconfig.GroupNetwork, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item := cloneGroupNetwork(input.Network)
	var err error
	item, err = groupconfig.NormalizeStoredNetwork(item)
	if err != nil {
		return groupconfig.GroupNetwork{}, false, err
	}
	key := input.CreatedBy + "\x00group.network\x00" + item.GroupID + "\x00" + input.IdempotencyKey
	var replay groupconfig.GroupNetwork
	if ok, err := s.replayBusinessLocked(key, input.RequestSHA256, &replay); ok || err != nil {
		return cloneGroupNetwork(replay), ok, err
	}
	previous := s.groupNetworks[item.GroupID]
	if previous.Revision != input.ExpectedRevision || item.Revision != input.ExpectedRevision+1 {
		return groupconfig.GroupNetwork{}, false, fmt.Errorf("%w: group network changed; reload before saving", faults.ErrConflict)
	}
	if err := s.validateGroupNetworkLocked(item); err != nil {
		return groupconfig.GroupNetwork{}, false, err
	}
	for _, pool := range s.endpointPools {
		if pool.GroupID == item.GroupID && pool.RuleID != "" && !item.ContainsPort(pool.Port) {
			return groupconfig.GroupNetwork{}, false, fmt.Errorf("%w: network change would invalidate a bound service endpoint", faults.ErrConflict)
		}
	}
	for _, rule := range s.forwardRules {
		customer := s.customers[rule.CustomerID]
		adminOwned := rule.OwnerKind == forwarding.OwnerAdministrator
		if rule.EntryGroupID == item.GroupID {
			policy := item.EffectiveDirectPolicy()
			if !item.ContainsPort(rule.ListenPort) ||
				(!adminOwned && len(item.AllowedUserGroupIDs) > 0 && !slices.Contains(item.AllowedUserGroupIDs, customer.UserGroupID)) ||
				(rule.EgressMode == forwarding.EgressDirect && !policy.AllowsDirect()) ||
				(rule.EgressMode == forwarding.EgressExitGroup && (!policy.AllowsExitGroup() || !slices.Contains(item.AllowedExitGroupIDs, rule.ExitGroupID))) {
				return groupconfig.GroupNetwork{}, false, fmt.Errorf("%w: network policy would invalidate an existing forwarding rule", faults.ErrConflict)
			}
		}
		if rule.ExitGroupID == item.GroupID && ((len(item.AllowedEntryGroupIDs) > 0 && !slices.Contains(item.AllowedEntryGroupIDs, rule.EntryGroupID)) ||
			(!adminOwned && len(item.AllowedUserGroupIDs) > 0 && !slices.Contains(item.AllowedUserGroupIDs, customer.UserGroupID))) {
			return groupconfig.GroupNetwork{}, false, fmt.Errorf("%w: exit policy would invalidate an existing forwarding rule", faults.ErrConflict)
		}
	}
	if err := s.recordBusinessLocked(key, input.RequestSHA256, item.GroupID, item); err != nil {
		return groupconfig.GroupNetwork{}, false, err
	}
	s.groupNetworks[item.GroupID] = cloneGroupNetwork(item)
	// A bound endpoint is the customer-visible address for its forwarding rule.
	// Keep it in lockstep with the entry group's address so an IP-to-domain
	// change does not invalidate existing subscriptions or endpoint bindings.
	updatedAt := time.Now().UTC()
	for poolID, pool := range s.endpointPools {
		if pool.GroupID != item.GroupID || pool.RuleID == "" || pool.Hostname == item.ConnectHost {
			continue
		}
		pool.Hostname = item.ConnectHost
		pool.UpdatedAt = updatedAt
		s.endpointPools[poolID] = pool
	}
	s.appendAuditLocked(event)
	return item, false, nil
}

func (s *Store) validateGroupNetworkLocked(item groupconfig.GroupNetwork) error {
	group, found := s.deviceGroups[item.GroupID]
	if !found {
		return faults.ErrNotFound
	}
	if !group.Kind.Creatable() {
		return fmt.Errorf("%w: legacy device group types are read-only", faults.ErrValidation)
	}
	if group.Kind != groups.KindExit && (item.ConnectHost == "" || len(item.EffectivePortRanges()) == 0) {
		return fmt.Errorf("%w: entry groups require a connect host and a nonzero port range", faults.ErrValidation)
	}
	policy := item.EffectiveDirectPolicy()
	if !policy.Valid() {
		return groupconfig.ErrInvalidDirectPolicy
	}
	if group.Kind == groups.KindExit && (item.ConnectHost != "" || len(item.EffectivePortRanges()) != 0) {
		return fmt.Errorf("%w: an EXIT group cannot have a connect host or listener port range", faults.ErrValidation)
	}
	if group.Kind == groups.KindExit && (policy != groupconfig.DirectPolicyDisabled || len(item.AllowedExitGroupIDs) != 0 || item.FallbackExitGroupID != "") {
		return fmt.Errorf("%w: an EXIT group cannot have entry forwarding permissions", faults.ErrValidation)
	}
	if group.Kind != groups.KindExit && len(item.AllowedEntryGroupIDs) != 0 {
		return fmt.Errorf("%w: an ENTRY group cannot restrict entry groups", faults.ErrValidation)
	}
	if group.Kind != groups.KindExit && policy == groupconfig.DirectPolicyForced && (len(item.AllowedExitGroupIDs) != 0 || item.FallbackExitGroupID != "") {
		return fmt.Errorf("%w: a FORCED direct entry group cannot authorize exit groups", faults.ErrValidation)
	}
	for _, id := range item.AllowedExitGroupIDs {
		if id == item.GroupID {
			return fmt.Errorf("%w: entry and exit groups must differ", faults.ErrValidation)
		}
	}
	if item.FallbackExitGroupID != "" && !slices.Contains(item.AllowedExitGroupIDs, item.FallbackExitGroupID) {
		return fmt.Errorf("%w: fallback exit must also be in allowed_exit_group_ids", faults.ErrValidation)
	}
	if err := s.validateAuthorizedGroupsLocked(item.AllowedExitGroupIDs, false); err != nil {
		return err
	}
	if err := s.validateAuthorizedGroupsLocked(item.AllowedEntryGroupIDs, true); err != nil {
		return err
	}
	for _, id := range item.AllowedUserGroupIDs {
		if _, exists := s.userGroups[id]; !exists {
			return fmt.Errorf("%w: authorized user group does not exist", faults.ErrValidation)
		}
	}
	return nil
}

func cloneGroupNetwork(item groupconfig.GroupNetwork) groupconfig.GroupNetwork {
	item.PortRanges = append([]groupconfig.PortRange{}, item.PortRanges...)
	item.AllowedUserGroupIDs = append([]string{}, item.AllowedUserGroupIDs...)
	item.AllowedEntryGroupIDs = append([]string{}, item.AllowedEntryGroupIDs...)
	item.AllowedExitGroupIDs = append([]string{}, item.AllowedExitGroupIDs...)
	if normalized, err := groupconfig.NormalizeStoredNetwork(item); err == nil {
		item = normalized
	}
	return item
}
