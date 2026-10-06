package memoryrepo

import (
	"context"
	"crypto/subtle"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/faults"
)

func (s *Store) AdministratorByUsername(_ context.Context, username string) (auth.Administrator, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	admin, exists := s.adminsByUsername[username]
	if !exists {
		return auth.Administrator{}, faults.ErrNotFound
	}
	return cloneAdministrator(admin), nil
}

func (s *Store) AdministratorByID(_ context.Context, id string) (auth.Administrator, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, admin := range s.adminsByUsername {
		if admin.ID == id {
			return cloneAdministrator(admin), nil
		}
	}
	return auth.Administrator{}, faults.ErrNotFound
}

func (s *Store) UpdateAdministratorPassword(_ context.Context, id string, expectedHash, newHash []byte, event audit.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for username, admin := range s.adminsByUsername {
		if admin.ID != id {
			continue
		}
		if subtle.ConstantTimeCompare(admin.PasswordHash, expectedHash) != 1 {
			return faults.ErrConflict
		}
		admin.PasswordHash = append([]byte(nil), newHash...)
		s.adminsByUsername[username] = admin
		for hash, session := range s.sessionsByHash {
			if session.AdminID == id {
				delete(s.sessionsByHash, hash)
			}
		}
		s.appendAuditLocked(event)
		return nil
	}
	return faults.ErrNotFound
}

func (s *Store) CreateSession(_ context.Context, session auth.Session, event audit.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.sessionsByHash[session.TokenHash]; exists {
		return faults.ErrConflict
	}
	s.sessionsByHash[session.TokenHash] = session
	s.appendAuditLocked(event)
	return nil
}

func (s *Store) SessionByTokenHash(_ context.Context, tokenHash string, now time.Time) (auth.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, exists := s.sessionsByHash[tokenHash]
	if !exists || !now.Before(session.ExpiresAt) {
		return auth.Session{}, faults.ErrUnauthorized
	}
	return session, nil
}
