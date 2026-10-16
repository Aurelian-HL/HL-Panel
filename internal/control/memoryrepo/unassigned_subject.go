package memoryrepo

import (
	"context"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
)

func (s *Store) hasNonCustomerSubjectLocked(r forwarding.Rule) bool {
	return r.IsUnassignedImport() || s.hasAdministratorSubjectLocked(r)
}

func (s *Store) ValidateUnassignedSubject(_ context.Context, subject string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.forwardRules {
		if r.CustomerID == subject && (r.IsUnassignedImport() || r.OwnerKind == forwarding.OwnerAdministrator && s.importedRuleManagedByLocked(r, r.OwnerID) && s.hasAdministratorSubjectLocked(r)) {
			return nil
		}
	}
	return faults.ErrNotFound
}
