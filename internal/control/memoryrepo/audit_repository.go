package memoryrepo

import (
	"context"

	"github.com/hongle/hl-panel/internal/control/audit"
)

func (s *Store) AppendAudit(_ context.Context, event audit.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendAuditLocked(event)
	return nil
}

func (s *Store) AuditEvents() []audit.Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]audit.Event, len(s.auditEvents))
	for index := range s.auditEvents {
		items[index] = cloneAuditEvent(s.auditEvents[index])
	}
	return items
}

func (s *Store) appendAuditLocked(event audit.Event) {
	s.auditEvents = append(s.auditEvents, cloneAuditEvent(event))
}
