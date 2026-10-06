package memoryrepo

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

func (s *Store) CreateEnrollmentToken(_ context.Context, token enrollment.Token, event audit.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token.GroupID != "" {
		if _, exists := s.deviceGroups[token.GroupID]; !exists {
			return fmt.Errorf("%w: enrollment group does not exist", faults.ErrValidation)
		}
	}
	if token.NezhaServerID != 0 {
		if token.GroupID == "" {
			return fmt.Errorf("%w: Nezha binding requires a group", faults.ErrValidation)
		}
		for _, node := range s.nodes {
			if node.NezhaServerID == token.NezhaServerID {
				return fmt.Errorf("%w: Nezha server is already bound", faults.ErrConflict)
			}
		}
		for _, existing := range s.tokensByHash {
			if existing.NezhaServerID == token.NezhaServerID && existing.UsedAt == nil && existing.RevokedAt == nil && token.CreatedAt.Before(existing.ExpiresAt) {
				return fmt.Errorf("%w: Nezha server has a pending enrollment", faults.ErrConflict)
			}
		}
	}
	if _, exists := s.tokensByHash[token.TokenHash]; exists {
		return faults.ErrConflict
	}
	s.tokensByHash[token.TokenHash] = token
	s.appendAuditLocked(event)
	return nil
}

func (s *Store) ListPendingEnrollmentTokens(_ context.Context, groupID string, now time.Time) ([]enrollment.PendingToken, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, exists := s.deviceGroups[groupID]; !exists {
		return nil, faults.ErrNotFound
	}
	items := make([]enrollment.PendingToken, 0)
	for _, token := range s.tokensByHash {
		if token.GroupID != groupID || token.UsedAt != nil || token.RevokedAt != nil || !now.Before(token.ExpiresAt) {
			continue
		}
		items = append(items, enrollment.PendingToken{ID: token.ID, Name: token.Name, GroupID: token.GroupID, ExpiresAt: token.ExpiresAt, CreatedAt: token.CreatedAt, NezhaServerID: token.NezhaServerID})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	return items, nil
}

func (s *Store) ConsumeEnrollmentToken(_ context.Context, input enrollment.ConsumeInput, now time.Time, event audit.Event) (nodes.Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	token, exists := s.tokensByHash[input.TokenHash]
	if !exists {
		return nodes.Node{}, faults.ErrUnauthorized
	}
	if token.UsedAt != nil {
		return nodes.Node{}, faults.ErrAlreadyUsed
	}
	if token.RevokedAt != nil {
		return nodes.Node{}, fmt.Errorf("%w: enrollment token has been revoked", faults.ErrConflict)
	}
	if !now.Before(token.ExpiresAt) {
		return nodes.Node{}, faults.ErrExpired
	}
	if _, exists := s.nodes[input.Node.ID]; exists {
		return nodes.Node{}, faults.ErrConflict
	}
	if _, exists := s.nodesByCredential[input.CredentialHash]; exists {
		return nodes.Node{}, faults.ErrConflict
	}
	if token.NezhaServerID != 0 {
		for _, existing := range s.nodes {
			if existing.NezhaServerID == token.NezhaServerID {
				return nodes.Node{}, fmt.Errorf("%w: Nezha server is already bound", faults.ErrConflict)
			}
		}
	}

	usedAt := now
	token.UsedAt = &usedAt
	node := cloneNode(input.Node)
	node.Name = token.Name
	node.NezhaServerID = token.NezhaServerID
	node.CredentialHash = input.CredentialHash
	s.nodes[node.ID] = node
	s.nodesByCredential[input.CredentialHash] = node.ID
	if token.GroupID != "" {
		if _, _, err := s.upsertGroupMemberLocked(groups.Member{GroupID: token.GroupID, NodeID: node.ID, DialHost: node.Hostname, Weight: 100, Priority: 0, CreatedAt: now, UpdatedAt: now}); err != nil {
			delete(s.nodes, node.ID)
			delete(s.nodesByCredential, input.CredentialHash)
			return nodes.Node{}, err
		}
	}
	s.tokensByHash[input.TokenHash] = token
	event.ActorID = token.ID
	if token.NezhaServerID != 0 {
		event.Metadata["nezha_server_id"] = token.NezhaServerID
	}
	s.appendAuditLocked(event)
	return cloneNode(node), nil
}

func enrollmentTokenRevokeKey(input enrollment.RevokeInput) string {
	return input.RevokedBy + "\x00enrollment_token.revoke\x00" + input.ID + "\x00" + input.IdempotencyKey
}

func (s *Store) RevokeEnrollmentToken(_ context.Context, input enrollment.RevokeInput, event audit.Event) (enrollment.RevokeResult, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := enrollmentTokenRevokeKey(input)
	var replay enrollment.RevokeResult
	if replayed, err := s.replayBusinessLocked(key, input.RequestSHA256, &replay); err != nil || replayed {
		return replay, replayed, err
	}
	if input.ID == "" || input.RevokedBy == "" || input.IdempotencyKey == "" || input.RequestSHA256 == "" || input.RevokedAt.IsZero() {
		return enrollment.RevokeResult{}, false, fmt.Errorf("%w: invalid enrollment token revoke mutation", faults.ErrValidation)
	}
	var tokenHash string
	var token enrollment.Token
	for hash, candidate := range s.tokensByHash {
		if candidate.ID == input.ID {
			tokenHash, token = hash, candidate
			break
		}
	}
	if tokenHash == "" {
		return enrollment.RevokeResult{}, false, faults.ErrNotFound
	}
	if token.UsedAt != nil {
		return enrollment.RevokeResult{}, false, fmt.Errorf("%w: used enrollment token cannot be revoked", faults.ErrConflict)
	}
	if token.RevokedAt != nil {
		return enrollment.RevokeResult{}, false, fmt.Errorf("%w: enrollment token is already revoked", faults.ErrConflict)
	}
	revokedAt := input.RevokedAt.UTC()
	result := enrollment.RevokeResult{ID: token.ID, RevokedAt: revokedAt}
	if err := s.recordBusinessLocked(key, input.RequestSHA256, token.ID, result); err != nil {
		return enrollment.RevokeResult{}, false, err
	}
	token.RevokedAt = &revokedAt
	s.tokensByHash[tokenHash] = token
	s.appendAuditLocked(event)
	return result, false, nil
}
