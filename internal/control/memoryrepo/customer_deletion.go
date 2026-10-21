package memoryrepo

import (
	"context"
	"fmt"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
)

var _ customers.CustomerDeletionRepository = (*Store)(nil)

func (s *Store) DeleteCustomer(_ context.Context, input customers.DeleteInput, event audit.Event) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if input.ID == "" || input.DeletedBy == "" || input.IdempotencyKey == "" || input.RequestSHA256 == "" || input.ExpectedRevision < 1 {
		return false, fmt.Errorf("%w: deletion actor, ID, revision and idempotency metadata are required", faults.ErrValidation)
	}
	key := input.DeletedBy + "\x00customer.delete\x00" + input.IdempotencyKey
	var result bool
	if replayed, err := s.replayBusinessLocked(key, input.RequestSHA256, &result); replayed || err != nil {
		return replayed, err
	}
	item, exists := s.customers[input.ID]
	if !exists {
		return false, faults.ErrNotFound
	}
	if item.Revision != input.ExpectedRevision {
		return false, fmt.Errorf("%w: customer changed; reload before deleting", faults.ErrConflict)
	}
	for _, rule := range s.forwardRules {
		if rule.CustomerID == input.ID || rule.OwnerKind == forwarding.OwnerCustomer && rule.OwnerID == input.ID {
			return false, fmt.Errorf("%w: customer has forwarding rules; delete or transfer its rules first", faults.ErrConflict)
		}
	}
	for _, record := range s.subscriptions {
		if record.Item.CustomerID == input.ID {
			return false, fmt.Errorf("%w: customer has subscriptions; delete its subscriptions first", faults.ErrConflict)
		}
	}
	for _, record := range s.vlessBindings {
		if record.Binding.CustomerID == input.ID && record.Binding.State != vlessidentity.StateRevoked {
			return false, fmt.Errorf("%w: customer has VLESS identities; delete their forwarding rules first", faults.ErrConflict)
		}
	}
	for _, material := range s.vlessRuntimeMaterials {
		if material.CustomerID == input.ID && material.State != vlessruntime.StateRevoked {
			return false, fmt.Errorf("%w: customer has VLESS runtime material; delete its forwarding rules first", faults.ErrConflict)
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
	s.appendAuditLocked(event)
	return false, nil
}
