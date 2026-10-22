package memoryrepo

import (
	"context"
	"fmt"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/faults"
)

var _ customers.DeletionRepository = (*Store)(nil)

func (s *Store) DeleteCustomer(_ context.Context, input customers.DeleteInput, event audit.Event) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, replayed, err := s.customerDeletionReplayLocked(input, "customer.delete")
	if replayed || err != nil {
		return replayed, err
	}
	item, exists := s.customers[input.ID]
	_, hasLogin := s.panelAccounts[input.ID]
	if !exists || !hasLogin {
		return false, faults.ErrNotFound
	}
	if item.Revision != input.ExpectedRevision {
		return false, fmt.Errorf("%w: customer changed; reload before deleting", faults.ErrConflict)
	}
	if err := s.recordBusinessLocked(key, input.RequestSHA256, input.ID, true); err != nil {
		return false, err
	}
	// Retain the service owner and every associated quota, rule and credential.
	// Login deletion is not a forwarding mutation and must publish no generation.
	delete(s.panelAccounts, input.ID)
	for hash, session := range s.customerSessions {
		if session.CustomerID == input.ID {
			delete(s.customerSessions, hash)
		}
	}
	s.appendAuditLocked(event)
	return false, nil
}

func (s *Store) DeleteUserGroup(_ context.Context, input customers.DeleteInput, event audit.Event) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, replayed, err := s.customerDeletionReplayLocked(input, "user_group.delete")
	if replayed || err != nil {
		return replayed, err
	}
	item, exists := s.userGroups[input.ID]
	if !exists {
		return false, faults.ErrNotFound
	}
	if item.Revision != input.ExpectedRevision {
		return false, fmt.Errorf("%w: user group changed; reload before deleting", faults.ErrConflict)
	}
	for _, customer := range s.customers {
		if customer.UserGroupID == input.ID {
			return false, fmt.Errorf("%w: user group has customers; move or delete its customers first", faults.ErrConflict)
		}
	}
	for _, group := range s.deviceGroups {
		if group.UserGroupID == input.ID {
			return false, fmt.Errorf("%w: user group is referenced by a device group", faults.ErrConflict)
		}
	}
	for _, network := range s.groupNetworks {
		if containsString(network.AllowedUserGroupIDs, input.ID) {
			return false, fmt.Errorf("%w: user group is referenced by a network policy", faults.ErrConflict)
		}
	}
	if err := s.recordBusinessLocked(key, input.RequestSHA256, input.ID, true); err != nil {
		return false, err
	}
	delete(s.userGroups, input.ID)
	s.appendAuditLocked(event)
	return false, nil
}

func (s *Store) customerDeletionReplayLocked(input customers.DeleteInput, operation string) (string, bool, error) {
	if input.ID == "" || input.DeletedBy == "" || input.IdempotencyKey == "" || input.RequestSHA256 == "" || input.ExpectedRevision < 1 {
		return "", false, fmt.Errorf("%w: deletion actor, ID, revision and idempotency metadata are required", faults.ErrValidation)
	}
	key := input.DeletedBy + "\x00" + operation + "\x00" + input.IdempotencyKey
	var result bool
	replayed, err := s.replayBusinessLocked(key, input.RequestSHA256, &result)
	return key, replayed, err
}
