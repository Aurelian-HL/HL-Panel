package memoryrepo

import (
	"context"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

// IdentityReadView is an immutable, detached projection of a validated store.
// It exposes only identity reads: no mutations or transaction-bound history
// reader can be attached to it. Results retain the normal repository cloning
// and session-expiry semantics.
type IdentityReadView struct {
	state *Store
}

func (s *Store) IdentityReadView() *IdentityReadView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := &Store{
		adminsByUsername:  make(map[string]auth.Administrator, len(s.adminsByUsername)),
		sessionsByHash:    make(map[string]auth.Session, len(s.sessionsByHash)),
		nodes:             make(map[string]nodes.Node, len(s.nodes)),
		nodesByCredential: make(map[string]string, len(s.nodesByCredential)),
	}
	for username, admin := range s.adminsByUsername {
		state.adminsByUsername[username] = cloneAdministrator(admin)
	}
	for hash, session := range s.sessionsByHash {
		state.sessionsByHash[hash] = session
	}
	for id, node := range s.nodes {
		state.nodes[id] = cloneNode(node)
	}
	for hash, id := range s.nodesByCredential {
		state.nodesByCredential[hash] = id
	}
	return &IdentityReadView{state: state}
}

func (v *IdentityReadView) AdministratorByUsername(ctx context.Context, username string) (auth.Administrator, error) {
	return v.state.AdministratorByUsername(ctx, username)
}

func (v *IdentityReadView) AdministratorByID(ctx context.Context, id string) (auth.Administrator, error) {
	return v.state.AdministratorByID(ctx, id)
}

func (v *IdentityReadView) SessionByTokenHash(ctx context.Context, hash string, now time.Time) (auth.Session, error) {
	return v.state.SessionByTokenHash(ctx, hash, now)
}

func (v *IdentityReadView) NodeByCredentialHash(ctx context.Context, hash string) (nodes.Node, error) {
	return v.state.NodeByCredentialHash(ctx, hash)
}
