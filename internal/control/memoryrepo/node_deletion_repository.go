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
	// Recheck under the same lock as heartbeat writes, including future clocks.
	if node.LastHeartbeatAt != nil && input.DeletedAt.Sub(*node.LastHeartbeatAt) <= input.OnlineFor {
		return result, false, fmt.Errorf("%w: 节点仍在线，请等待节点离线后再删除", faults.ErrConflict)
	}
	result = nodes.DeleteResult{NodeID: node.ID, DeletedAt: input.DeletedAt.UTC()}
	if err := s.recordBusinessLocked(key, input.RequestSHA256, node.ID, result); err != nil {
		return result, false, err
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
