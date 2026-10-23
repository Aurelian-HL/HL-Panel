package memoryrepo

import (
	"context"
	"fmt"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
)

var _ customers.ServiceOwnerDeletionRepository = (*Store)(nil)

func (s *Store) DeleteServiceOwner(ctx context.Context, input customers.DeleteInput, event audit.Event) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	key, replayed, err := s.customerDeletionReplayLocked(input, "service_owner.delete")
	if replayed || err != nil {
		return replayed, err
	}
	item, exists := s.customers[input.ID]
	if !exists {
		return false, faults.ErrNotFound
	}
	if _, hasLogin := s.panelAccounts[input.ID]; hasLogin {
		return false, fmt.Errorf("%w: service owner has a panel login; revoke the login separately first", faults.ErrConflict)
	}
	if item.Revision != input.ExpectedRevision {
		return false, fmt.Errorf("%w: service owner changed; reload before deleting", faults.ErrConflict)
	}
	for _, rule := range s.forwardRules {
		if rule.CustomerID == input.ID || rule.OwnerID == input.ID {
			return false, fmt.Errorf("%w: service owner is referenced by a forwarding rule", faults.ErrConflict)
		}
	}
	for _, subscription := range s.subscriptions {
		if subscription.Item.CustomerID == input.ID || subscription.OwnerID == input.ID {
			return false, fmt.Errorf("%w: service owner is referenced by a subscription", faults.ErrConflict)
		}
	}
	for _, record := range s.vlessBindings {
		if record.Binding.CustomerID == input.ID && record.Binding.State != vlessidentity.StateRevoked {
			return false, fmt.Errorf("%w: service owner has a non-revoked VLESS identity", faults.ErrConflict)
		}
	}
	for _, material := range s.vlessRuntimeMaterials {
		if material.CustomerID == input.ID && material.State != vlessruntime.StateRevoked {
			return false, fmt.Errorf("%w: service owner has non-revoked VLESS runtime material", faults.ErrConflict)
		}
	}
	if err := s.recordBusinessLocked(key, input.RequestSHA256, input.ID, true); err != nil {
		return false, err
	}
	delete(s.customers, input.ID)
	for hash, session := range s.customerSessions {
		if session.CustomerID == input.ID {
			delete(s.customerSessions, hash)
		}
	}
	// Revoked identity history, rules, subscriptions and node generations stay
	// unchanged. The owner is removed only after every live reference is absent.
	s.appendAuditLocked(event)
	return false, nil
}
