package memoryrepo

import (
	"fmt"
	"sort"
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
		if previous.DeletedAt == nil && token.UsedNodeID == previous.ID && joined && member.RetiredAt == nil {
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
	node.DeletedAt = nil
	if previous.DeletedAt != nil {
		node.LastHeartbeatAt = nil
		node.Resources = nil
	}
	node.Name, node.Hostname, node.DialHost = token.Name, input.Node.Hostname, input.Node.DialHost
	node.Platform, node.Architecture, node.AgentVersion = input.Node.Platform, input.Node.Architecture, input.Node.AgentVersion
	node.UpdatedAt = now
	previousMembers := make(map[string]groups.Member)
	groupIDs := []string{token.GroupID}
	for groupID, members := range s.membersByGroup {
		if member, exists := members[node.ID]; exists {
			previousMembers[groupID] = member
			if groupID != token.GroupID && member.RetiredAt == nil {
				groupIDs = append(groupIDs, groupID)
			}
		}
	}
	// Capture every affected projection before changing any membership. Related
	// UDP groups can overlap; a later snapshot must not capture partial changes.
	sort.Strings(groupIDs)
	mutations := make([]groupMemberMutationSnapshot, 0, len(groupIDs))
	for _, groupID := range groupIDs {
		mutations = append(mutations, s.captureGroupMemberMutationLocked(groupID, node.ID))
	}
	restore := func() {
		for i := len(mutations) - 1; i >= 0; i-- {
			s.restoreGroupMemberMutationLocked(mutations[i])
		}
	}
	retired := make([]groups.Member, 0, len(groupIDs)-1)
	for _, groupID := range groupIDs {
		if groupID == token.GroupID {
			continue
		}
		member := previousMembers[groupID]
		member.RetiredAt, member.UpdatedAt = &now, now
		s.membersByGroup[groupID][node.ID] = member
		retired = append(retired, member)
	}
	member := groups.Member{GroupID: token.GroupID, NodeID: node.ID, DialHost: node.DialHost,
		Weight: 100, CreatedAt: now, UpdatedAt: now}
	if old, exists := previousMembers[token.GroupID]; exists {
		member.CreatedAt, member.Weight, member.Priority = old.CreatedAt, old.Weight, old.Priority
	}
	if err := s.validateForwardingMemberListenersLocked(member); err != nil {
		restore()
		return nodes.Node{}, err
	}
	s.membersByGroup[token.GroupID][node.ID] = member
	s.nodes[node.ID] = node
	// The target group's endpoint projection authorizes automatic Reality
	// material. Update it before compiling, with rollback on compilation failure.
	for _, changed := range append(retired, member) {
		s.syncGroupMemberToEndpointPoolsLocked(changed)
	}
	// Reinstallation follows the same native-import provisioning contract as a
	// normal group join, while preserving explicit node-local revocations.
	if _, err := s.provisionMemberNativeMaterialsLocked(member); err != nil {
		restore()
		return nodes.Node{}, err
	}
	// Compile once after both sides change so old listeners are removed before
	// installing the new group's listeners, including same-port group switches.
	// Moving or changing an exit address must also withdraw its old address
	// from dependent QUIC/KCP entries, without recompiling independent peers.
	if _, _, err := s.recompileMemberConfigsLocked(node.ID, groupIDs, now); err != nil {
		restore()
		return nodes.Node{}, err
	}
	for _, changed := range append(retired, member) {
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
