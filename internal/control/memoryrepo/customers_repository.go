package memoryrepo

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groups"
)

var _ customers.Repository = (*Store)(nil)

func (s *Store) ListCustomers(_ context.Context) ([]customers.Customer, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]customers.Customer, 0, len(s.customers))
	for _, item := range s.customers {
		items = append(items, cloneCustomer(item))
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Username == items[j].Username {
			return items[i].ID < items[j].ID
		}
		return items[i].Username < items[j].Username
	})
	return items, nil
}

func (s *Store) Customer(_ context.Context, id string) (customers.Customer, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, exists := s.customers[id]
	if !exists {
		return customers.Customer{}, faults.ErrNotFound
	}
	return cloneCustomer(item), nil
}

func (s *Store) SaveCustomer(_ context.Context, input customers.SaveCustomerInput, event audit.Event) (customers.Customer, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if input.IdempotencyKey == "" || input.RequestSHA256 == "" || input.CreatedBy == "" {
		return customers.Customer{}, false, fmt.Errorf("%w: mutation actor, idempotency key and request hash are required", faults.ErrValidation)
	}
	operation := "customer.create"
	if input.ExpectedRevision > 0 {
		operation = "customer.update"
	}
	key := input.CreatedBy + "\x00" + operation + "\x00" + input.IdempotencyKey
	var replay customers.Customer
	if ok, err := s.replayBusinessLocked(key, input.RequestSHA256, &replay); ok || err != nil {
		return replay, ok, err
	}
	item := cloneCustomer(input.Customer)
	previous, exists := s.customers[item.ID]
	if input.ExpectedRevision < 0 || input.ExpectedRevision == math.MaxInt64 || item.Revision != input.ExpectedRevision+1 || exists && input.ExpectedRevision == 0 || !exists && input.ExpectedRevision > 0 || exists && previous.Revision != input.ExpectedRevision {
		return customers.Customer{}, false, fmt.Errorf("%w: customer changed; reload before saving", faults.ErrConflict)
	}
	userGroup, groupExists := s.userGroups[item.UserGroupID]
	if item.UserGroupID != "" && !groupExists {
		return customers.Customer{}, false, fmt.Errorf("%w: user group does not exist", faults.ErrValidation)
	}
	ownedRules := 0
	for _, rule := range s.forwardRules {
		if rule.CustomerID != item.ID {
			continue
		}
		ownedRules++
		if !userGroupAuthorizesRule(userGroup, rule) {
			return customers.Customer{}, false, fmt.Errorf("%w: user group change would revoke an existing rule; edit its rules first", faults.ErrConflict)
		}
	}
	if item.MaxRules > 0 && ownedRules > item.MaxRules {
		return customers.Customer{}, false, fmt.Errorf("%w: max_rules is lower than the customer's existing rule count", faults.ErrConflict)
	}
	for id, other := range s.customers {
		if id != item.ID && strings.EqualFold(other.Username, item.Username) {
			return customers.Customer{}, false, fmt.Errorf("%w: username already exists", faults.ErrConflict)
		}
	}
	if exists {
		// Usage is owned by accounting, including when an observation arrives
		// between the service read and this transaction's optimistic update.
		item.CreatedAt, item.TrafficUsedBytes = previous.CreatedAt, previous.TrafficUsedBytes
		if !input.PasswordChanged {
			item.PasswordHash = append([]byte(nil), previous.PasswordHash...)
		}
	} else {
		item.TrafficUsedBytes = 0
	}
	if err := auth.ValidatePasswordHash(item.PasswordHash); err != nil {
		return customers.Customer{}, false, err
	}
	if err := customers.ValidateStoredCustomer(item); err != nil {
		return customers.Customer{}, false, err
	}
	item.Status = ""
	if err := s.recordBusinessLocked(key, input.RequestSHA256, item.ID, item); err != nil {
		return customers.Customer{}, false, err
	}
	s.customers[item.ID] = cloneCustomer(item)
	s.appendAuditLocked(event)
	return cloneCustomer(item), false, nil
}

func (s *Store) ListUserGroups(_ context.Context) ([]customers.UserGroup, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]customers.UserGroup, 0, len(s.userGroups))
	for _, item := range s.userGroups {
		items = append(items, cloneUserGroup(item))
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Name == items[j].Name {
			return items[i].ID < items[j].ID
		}
		return items[i].Name < items[j].Name
	})
	return items, nil
}

func (s *Store) UserGroup(_ context.Context, id string) (customers.UserGroup, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, exists := s.userGroups[id]
	if !exists {
		return customers.UserGroup{}, faults.ErrNotFound
	}
	return cloneUserGroup(item), nil
}

func (s *Store) SaveUserGroup(_ context.Context, input customers.SaveUserGroupInput, event audit.Event) (customers.UserGroup, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if input.IdempotencyKey == "" || input.RequestSHA256 == "" || input.CreatedBy == "" {
		return customers.UserGroup{}, false, fmt.Errorf("%w: mutation actor, idempotency key and request hash are required", faults.ErrValidation)
	}
	operation := "user_group.create"
	if input.ExpectedRevision > 0 {
		operation = "user_group.update"
	}
	key := input.CreatedBy + "\x00" + operation + "\x00" + input.IdempotencyKey
	var replay customers.UserGroup
	if ok, err := s.replayBusinessLocked(key, input.RequestSHA256, &replay); ok || err != nil {
		return replay, ok, err
	}
	item := cloneUserGroup(input.UserGroup)
	previous, exists := s.userGroups[item.ID]
	if input.ExpectedRevision < 0 || input.ExpectedRevision == math.MaxInt64 || item.Revision != input.ExpectedRevision+1 || exists && input.ExpectedRevision == 0 || !exists && input.ExpectedRevision > 0 || exists && previous.Revision != input.ExpectedRevision {
		return customers.UserGroup{}, false, fmt.Errorf("%w: user group changed; reload before saving", faults.ErrConflict)
	}
	for id, other := range s.userGroups {
		if id != item.ID && strings.EqualFold(other.Name, item.Name) {
			return customers.UserGroup{}, false, fmt.Errorf("%w: user group name already exists", faults.ErrConflict)
		}
	}
	if err := s.validateAuthorizedGroupsLocked(item.AllowedEntryGroupIDs, true); err != nil {
		return customers.UserGroup{}, false, err
	}
	if err := s.validateAuthorizedGroupsLocked(item.AllowedExitGroupIDs, false); err != nil {
		return customers.UserGroup{}, false, err
	}
	for _, rule := range s.forwardRules {
		customer, found := s.customers[rule.CustomerID]
		if found && customer.UserGroupID == item.ID && !userGroupAuthorizesRule(item, rule) {
			return customers.UserGroup{}, false, fmt.Errorf("%w: authorization change would revoke an existing rule; edit its rules first", faults.ErrConflict)
		}
	}
	if exists {
		item.CreatedAt = previous.CreatedAt
	}
	if err := customers.ValidateStoredUserGroup(item); err != nil {
		return customers.UserGroup{}, false, err
	}
	if err := s.recordBusinessLocked(key, input.RequestSHA256, item.ID, item); err != nil {
		return customers.UserGroup{}, false, err
	}
	s.userGroups[item.ID] = cloneUserGroup(item)
	s.appendAuditLocked(event)
	return cloneUserGroup(item), false, nil
}

func (s *Store) validateAuthorizedGroupsLocked(ids []string, entry bool) error {
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			return fmt.Errorf("%w: authorized device group ID is duplicated", faults.ErrValidation)
		}
		seen[id] = true
		group, exists := s.deviceGroups[id]
		if !exists {
			return fmt.Errorf("%w: authorized device group does not exist", faults.ErrValidation)
		}
		allowed := group.Kind == groups.KindExit
		if entry {
			allowed = group.Kind == groups.KindEntry
		}
		if !allowed {
			return fmt.Errorf("%w: authorized device group has the wrong entry/exit role", faults.ErrValidation)
		}
	}
	return nil
}

func cloneCustomer(item customers.Customer) customers.Customer {
	item.PasswordHash = append([]byte(nil), item.PasswordHash...)
	if item.ExpiresAt != nil {
		expires := *item.ExpiresAt
		item.ExpiresAt = &expires
	}
	return item
}

func cloneUserGroup(item customers.UserGroup) customers.UserGroup {
	item.AllowedEntryGroupIDs = append([]string{}, item.AllowedEntryGroupIDs...)
	item.AllowedExitGroupIDs = append([]string{}, item.AllowedExitGroupIDs...)
	return item
}

func userGroupAuthorizesRule(group customers.UserGroup, rule forwarding.Rule) bool {
	if !slices.Contains(group.AllowedEntryGroupIDs, rule.EntryGroupID) {
		return false
	}
	if rule.EgressMode == forwarding.EgressDirect {
		return group.AllowDirect
	}
	return rule.EgressMode == forwarding.EgressExitGroup && slices.Contains(group.AllowedExitGroupIDs, rule.ExitGroupID)
}
