package memoryrepo

import (
	"context"
	"sort"
)

func (s *Store) VLESSSetupAdministrator(_ context.Context, ruleID string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rule, ok := s.forwardRules[ruleID]
	if !ok {
		return "", nil
	}
	actors := make([]string, 0)
	for _, admin := range s.adminsByUsername {
		if rule.OwnedByAdministrator(admin.ID) || s.delegatedRuleManagedByLocked(rule, admin.ID) {
			actors = append(actors, admin.ID)
		}
	}
	sort.Strings(actors)
	if len(actors) == 0 {
		return "", nil
	}
	return actors[0], nil
}
