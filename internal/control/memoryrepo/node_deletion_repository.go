package memoryrepo

import (
	"context"
	"fmt"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

// Keep a private tombstone so historical configurations and traffic retain
// their identity. The snapshot write and all membership changes are atomic.
func (s *Store) DeleteOffline(_ context.Context, input nodes.DeleteInput, event audit.Event) (nodes.DeleteResult, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if input.NodeID == "" || input.DeletedBy == "" || input.IdempotencyKey == "" || input.RequestSHA256 == "" || input.DeletedAt.IsZero() || input.OnlineFor <= 0 {
		return nodes.DeleteResult{}, false, fmt.Errorf("%w: invalid node deletion", faults.ErrValidation)
	}
	key := input.DeletedBy + "\x00node.delete\x00" + input.NodeID + "\x00" + input.IdempotencyKey
	var result nodes.DeleteResult
	if replayed, err := s.replayBusinessLocked(key, input.RequestSHA256, &result); replayed || err != nil {
		return result, replayed, err
	}
	node, exists := s.nodes[input.NodeID]
	if !exists {
		return result, false, faults.ErrNotFound
	}
	if node.DeletedAt != nil {
		result = nodes.DeleteResult{NodeID: node.ID, DeletedAt: *node.DeletedAt}
		if err := s.recordBusinessLocked(key, input.RequestSHA256, node.ID, result); err != nil {
			return result, false, err
		}
		return result, true, nil
	}
	// UI classification is advisory. Recheck membership under the same lock as
	// enrollment, group changes and heartbeat writes before allowing online cleanup.
	if input.OnlyUnassigned {
		for _, members := range s.membersByGroup {
			if member, exists := members[node.ID]; exists && member.RetiredAt == nil {
				return result, false, fmt.Errorf("%w: 节点已加入设备组，请刷新后先退役再删除", faults.ErrConflict)
			}
		}
		for _, members := range s.endpointMembers {
			if _, exists := members[node.ID]; exists {
				return result, false, fmt.Errorf("%w: 节点仍被端点池引用，请先移除再删除", faults.ErrConflict)
			}
		}
	}
	if !input.OnlyUnassigned && node.LastHeartbeatAt != nil && input.DeletedAt.Sub(*node.LastHeartbeatAt) <= input.OnlineFor {
		return result, false, fmt.Errorf("%w: 节点仍在线，请等待节点离线后再删除", faults.ErrConflict)
	}
	result = nodes.DeleteResult{NodeID: node.ID, DeletedAt: input.DeletedAt.UTC()}
	if err := s.recordBusinessLocked(key, input.RequestSHA256, node.ID, result); err != nil {
		return result, false, err
	}
	snapshots := []groupMemberMutationSnapshot{}
	groupIDs := []string{}
	for groupID, members := range s.membersByGroup {
		if member, ok := members[node.ID]; ok && member.RetiredAt == nil {
			snapshots = append(snapshots, s.captureGroupMemberMutationLocked(groupID, node.ID))
			groupIDs = append(groupIDs, groupID)
		}
	}
	for groupID, members := range s.membersByGroup {
		member, joined := members[node.ID]
		if !joined || member.RetiredAt != nil {
			continue
		}
		member.RetiredAt, member.UpdatedAt = timePointer(result.DeletedAt), result.DeletedAt
		members[node.ID] = member
		s.syncGroupMemberToEndpointPoolsLocked(member)
		group := s.deviceGroups[groupID]
		group.UpdatedAt = result.DeletedAt
		s.deviceGroups[groupID] = group
	}
	if err := s.recompileForwardingGroupsLocked(groupIDs, result.DeletedAt); err != nil {
		for i := len(snapshots) - 1; i >= 0; i-- {
			s.restoreGroupMemberMutationLocked(snapshots[i])
		}
		delete(s.businessIdempotency, key)
		return nodes.DeleteResult{}, false, err
	}
	for poolID, members := range s.endpointMembers {
		if _, exists := members[node.ID]; exists {
			delete(members, node.ID)
			pool := s.endpointPools[poolID]
			pool.MemberCount, pool.UpdatedAt = len(members), result.DeletedAt
			s.endpointPools[poolID] = pool
		}
		delete(s.protocolHealth[poolID], node.ID)
	}
	node.DeletedAt, node.UpdatedAt = timePointer(result.DeletedAt), result.DeletedAt
	node.NezhaServerID = 0
	s.nodes[node.ID] = node
	s.appendAuditLocked(event)
	return result, false, nil
}
