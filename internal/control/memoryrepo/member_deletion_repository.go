package memoryrepo

import (
	"context"
	"fmt"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/groups"
)

var _ groups.RetiredMemberDeletionRepository = (*Store)(nil)

func (s *Store) DeleteRetiredGroupMember(_ context.Context, input groups.DeleteMemberInput, event audit.Event) (groups.DeleteMemberResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if input.DeletedBy == "" || input.GroupID == "" || input.NodeID == "" || input.IdempotencyKey == "" || input.RequestSHA256 == "" || input.ExpectedUpdatedAt.IsZero() {
		return groups.DeleteMemberResult{}, fmt.Errorf("%w: mutation identity and member timestamp are required", faults.ErrValidation)
	}
	key := input.DeletedBy + "\x00device_group.member_delete\x00" + input.GroupID + "\x00" + input.NodeID + "\x00" + input.IdempotencyKey
	var replay groups.DeleteMemberResult
	if ok, err := s.replayBusinessLocked(key, input.RequestSHA256, &replay); ok || err != nil {
		replay.Replayed = ok
		return replay, err
	}
	members := s.membersByGroup[input.GroupID]
	member, exists := members[input.NodeID]
	if !exists {
		return groups.DeleteMemberResult{}, faults.ErrNotFound
	}
	if member.RetiredAt == nil {
		return groups.DeleteMemberResult{}, fmt.Errorf("%w: only retired members can be deleted", faults.ErrConflict)
	}
	if !member.UpdatedAt.Equal(input.ExpectedUpdatedAt) {
		return groups.DeleteMemberResult{}, fmt.Errorf("%w: device group member changed; reload before deleting", faults.ErrConflict)
	}
	result := groups.DeleteMemberResult{GroupID: input.GroupID, NodeID: input.NodeID, Deleted: true}
	if err := s.recordBusinessLocked(key, input.RequestSHA256, input.GroupID+":"+input.NodeID, result); err != nil {
		return groups.DeleteMemberResult{}, err
	}
	// Retirement already removed this node from scheduling. Clean any stale
	// projection only in this group, without compiling or restarting the node.
	member.UpdatedAt = input.DeletedAt
	s.syncGroupMemberToEndpointPoolsLocked(member)
	delete(members, input.NodeID)
	s.appendAuditLocked(event)
	return result, nil
}
