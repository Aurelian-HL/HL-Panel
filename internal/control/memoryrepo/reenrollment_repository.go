package memoryrepo

import (
	"fmt"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

// Caller holds the store lock; PostgreSQL wraps this in its snapshot transaction.
func (s *Store) reenrollNodeLocked(input enrollment.ConsumeInput, token enrollment.Token, now time.Time, event audit.Event) (nodes.Node, error) {
	previous, exists := s.nodes[input.Node.ID]
	if !exists || previous.CredentialHash != input.CredentialHash || s.nodesByCredential[input.CredentialHash] != previous.ID {
		return nodes.Node{}, faults.ErrUnauthorized
	}
	if token.GroupID == "" {
		return nodes.Node{}, fmt.Errorf("%w: reinstall requires a device-group token", faults.ErrValidation)
	}
	if _, exists := s.deviceGroups[token.GroupID]; !exists {
		return nodes.Node{}, faults.ErrNotFound
	}
	if token.UsedAt != nil {
		member, joined := s.membersByGroup[token.GroupID][previous.ID]
		if token.UsedNodeID == previous.ID && joined && member.RetiredAt == nil {
			// A lost response or repeated command is harmless. A superseded token
			// never moves the machine back to an older group.
			return cloneNode(previous), nil
		}
		return nodes.Node{}, faults.ErrAlreadyUsed
	}
	if token.RevokedAt != nil {
		return nodes.Node{}, fmt.Errorf("%w: enrollment token has been revoked", faults.ErrConflict)
	}
	if !now.Before(token.ExpiresAt) {
		return nodes.Node{}, faults.ErrExpired
	}
	if token.NezhaServerID != 0 && token.NezhaServerID != previous.NezhaServerID {
		return nodes.Node{}, fmt.Errorf("%w: reinstall cannot change a monitoring binding", faults.ErrConflict)
	}
	// Preserve the current telemetry and usage even if a heartbeat arrived
	// between HTTP authentication and this transaction.
	node := cloneNode(previous)
	node.Name, node.Hostname, node.DialHost = token.Name, input.Node.Hostname, input.Node.DialHost
	node.Platform, node.Architecture, node.AgentVersion = input.Node.Platform, input.Node.Architecture, input.Node.AgentVersion
	node.UpdatedAt = now
	previousMembers := make(map[string]groups.Member)
	retired := make([]groups.Member, 0)
	for groupID, members := range s.membersByGroup {
		if member, exists := members[node.ID]; exists {
			previousMembers[groupID] = member
			if groupID != token.GroupID && member.RetiredAt == nil {
				member.RetiredAt, member.UpdatedAt = &now, now
				members[node.ID] = member
				retired = append(retired, member)
			}
		}
	}
	member := groups.Member{GroupID: token.GroupID, NodeID: node.ID, DialHost: node.DialHost,
		Weight: 100, CreatedAt: now, UpdatedAt: now}
	if old, exists := previousMembers[token.GroupID]; exists {
		member.CreatedAt, member.Weight, member.Priority = old.CreatedAt, old.Weight, old.Priority
	}
	restore := func() {
		for groupID, old := range previousMembers {
			s.membersByGroup[groupID][node.ID] = old
		}
		if _, exists := previousMembers[token.GroupID]; !exists {
			delete(s.membersByGroup[token.GroupID], node.ID)
		}
		s.nodes[node.ID] = previous
	}
	if err := s.validateForwardingMemberListenersLocked(member); err != nil {
		restore()
		return nodes.Node{}, err
	}
	s.membersByGroup[token.GroupID][node.ID] = member
	s.nodes[node.ID] = node
	// Compile once after both sides change so old listeners are removed before
	// installing the new group's listeners, including same-port group switches.
	if _, _, err := s.compileNodeConfigLocked(node.ID, now); err != nil {
		restore()
		return nodes.Node{}, err
	}
	for _, changed := range append(retired, member) {
		s.syncGroupMemberToEndpointPoolsLocked(changed)
		group := s.deviceGroups[changed.GroupID]
		group.UpdatedAt = now
		s.deviceGroups[group.ID] = group
	}
	token.UsedAt, token.UsedNodeID = &now, node.ID
	s.tokensByHash[input.TokenHash] = token
	event.Metadata["group_id"], event.Metadata["token_id"] = token.GroupID, token.ID
	event.Metadata["retired_group_count"] = len(retired)
	s.appendAuditLocked(event)
	return cloneNode(s.nodes[node.ID]), nil
}
