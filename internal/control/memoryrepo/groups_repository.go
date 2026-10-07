package memoryrepo

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
)

func (s *Store) CreateDeviceGroup(_ context.Context, group groups.DeviceGroup, event audit.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if group.UserGroupID != "" {
		if _, exists := s.userGroups[group.UserGroupID]; !exists {
			return fmt.Errorf("%w: user_group_id does not reference an existing user group", faults.ErrValidation)
		}
	}
	if _, exists := s.deviceGroups[group.ID]; exists {
		return faults.ErrConflict
	}
	for _, existing := range s.deviceGroups {
		if existing.Name == group.Name {
			return fmt.Errorf("%w: device group name already exists", faults.ErrConflict)
		}
	}
	s.deviceGroups[group.ID] = group
	s.membersByGroup[group.ID] = make(map[string]groups.Member)
	s.revisionByIdempotency[group.ID] = make(map[string]string)
	s.appendAuditLocked(event)
	return nil
}

func (s *Store) UpdateDeviceGroup(_ context.Context, input groups.UpdateInput, event audit.Event) (groups.DeviceGroup, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if input.CreatedBy == "" || input.IdempotencyKey == "" || input.RequestSHA256 == "" {
		return groups.DeviceGroup{}, false, fmt.Errorf("%w: mutation actor, idempotency key and request hash are required", faults.ErrValidation)
	}
	key := input.CreatedBy + "\x00device_group.update\x00" + input.IdempotencyKey
	var replay groups.DeviceGroup
	if ok, err := s.replayBusinessLocked(key, input.RequestSHA256, &replay); ok || err != nil {
		return replay, ok, err
	}
	previous, exists := s.deviceGroups[input.Group.ID]
	if !exists {
		return groups.DeviceGroup{}, false, faults.ErrNotFound
	}
	if previous.MetadataRevision != input.ExpectedRevision {
		return groups.DeviceGroup{}, false, fmt.Errorf("%w: device group changed; reload before saving", faults.ErrConflict)
	}
	if input.Group.UserGroupID != "" {
		if _, exists := s.userGroups[input.Group.UserGroupID]; !exists {
			return groups.DeviceGroup{}, false, fmt.Errorf("%w: user_group_id does not reference an existing user group", faults.ErrValidation)
		}
	}
	for id, other := range s.deviceGroups {
		if id != input.Group.ID && other.Name == input.Group.Name {
			return groups.DeviceGroup{}, false, fmt.Errorf("%w: device group name already exists", faults.ErrConflict)
		}
	}
	updated := input.Group
	updated.Kind, updated.CreatedAt, updated.MemberCount, updated.CurrentRevision = previous.Kind, previous.CreatedAt, previous.MemberCount, previous.CurrentRevision
	updated.MetadataRevision, updated.UpdatedAt = input.ExpectedRevision+1, input.Group.UpdatedAt
	s.deviceGroups[updated.ID] = updated
	if err := s.recordBusinessLocked(key, input.RequestSHA256, updated.ID, updated); err != nil {
		return groups.DeviceGroup{}, false, err
	}
	s.appendAuditLocked(event)
	return updated, false, nil
}

func (s *Store) DeleteDeviceGroup(_ context.Context, input groups.DeleteInput, event audit.Event) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if input.DeletedBy == "" || input.GroupID == "" || input.IdempotencyKey == "" || input.RequestSHA256 == "" {
		return false, fmt.Errorf("%w: mutation actor, group id, idempotency key and request hash are required", faults.ErrValidation)
	}
	key := input.DeletedBy + "\x00device_group.delete\x00" + input.GroupID + "\x00" + input.IdempotencyKey
	var replay bool
	if replayed, err := s.replayBusinessLocked(key, input.RequestSHA256, &replay); replayed || err != nil {
		return replayed, err
	}
	if _, exists := s.deviceGroups[input.GroupID]; !exists {
		return false, faults.ErrNotFound
	}

	// Deletion is intentionally non-cascading. Every relationship is checked
	// while holding the store lock so a successful delete cannot race a write.
	for _, member := range s.membersByGroup[input.GroupID] {
		if member.RetiredAt == nil {
			return false, fmt.Errorf("%w: device group has active members; retire or remove them first", faults.ErrConflict)
		}
	}
	if _, exists := s.groupNetworks[input.GroupID]; exists {
		return false, fmt.Errorf("%w: device group has a network policy", faults.ErrConflict)
	}
	for _, network := range s.groupNetworks {
		if network.FallbackExitGroupID == input.GroupID || containsString(network.AllowedEntryGroupIDs, input.GroupID) || containsString(network.AllowedExitGroupIDs, input.GroupID) {
			return false, fmt.Errorf("%w: device group is referenced by a network policy", faults.ErrConflict)
		}
	}
	for _, userGroup := range s.userGroups {
		if containsString(userGroup.AllowedEntryGroupIDs, input.GroupID) || containsString(userGroup.AllowedExitGroupIDs, input.GroupID) {
			return false, fmt.Errorf("%w: device group is referenced by a user group", faults.ErrConflict)
		}
	}
	for _, rule := range s.forwardRules {
		if rule.EntryGroupID == input.GroupID || rule.ExitGroupID == input.GroupID {
			return false, fmt.Errorf("%w: device group is referenced by a forwarding rule", faults.ErrConflict)
		}
	}
	for _, pool := range s.endpointPools {
		if pool.GroupID == input.GroupID {
			return false, fmt.Errorf("%w: device group is referenced by an endpoint pool", faults.ErrConflict)
		}
	}
	if len(s.revisionsByGroup[input.GroupID]) > 0 {
		return false, fmt.Errorf("%w: device group has published revisions", faults.ErrConflict)
	}

	if err := s.recordBusinessLocked(key, input.RequestSHA256, input.GroupID, true); err != nil {
		return false, err
	}
	delete(s.deviceGroups, input.GroupID)
	delete(s.membersByGroup, input.GroupID)
	delete(s.revisionsByGroup, input.GroupID)
	delete(s.revisionByIdempotency, input.GroupID)
	s.appendAuditLocked(event)
	return false, nil
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func (s *Store) ListDeviceGroups(_ context.Context) ([]groups.DeviceGroup, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]groups.DeviceGroup, 0, len(s.deviceGroups))
	for _, stored := range s.deviceGroups {
		group := stored
		for _, member := range s.membersByGroup[group.ID] {
			if member.RetiredAt == nil {
				group.MemberCount++
			}
		}
		items = append(items, group)
	}
	sort.Slice(items, func(left, right int) bool {
		if items[left].Name == items[right].Name {
			return items[left].ID < items[right].ID
		}
		return items[left].Name < items[right].Name
	})
	return items, nil
}

func (s *Store) ListGroupMembers(_ context.Context, groupID string) ([]groups.Member, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	members, exists := s.membersByGroup[groupID]
	if !exists {
		return nil, faults.ErrNotFound
	}
	items := make([]groups.Member, 0, len(members))
	for _, member := range members {
		items = append(items, cloneGroupMember(member))
	}
	sort.Slice(items, func(left, right int) bool { return items[left].NodeID < items[right].NodeID })
	return items, nil
}

func cloneGroupMember(member groups.Member) groups.Member {
	if member.RetiredAt != nil {
		retiredAt := *member.RetiredAt
		member.RetiredAt = &retiredAt
	}
	return member
}

func (s *Store) UpsertGroupMember(_ context.Context, member groups.Member, event audit.Event) (groups.Member, []generations.NodeConfigGeneration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, assignments, err := s.upsertGroupMemberLocked(member)
	if err != nil {
		return groups.Member{}, nil, err
	}
	s.appendAuditLocked(event)
	return stored, assignments, nil
}

func (s *Store) UpdateGroupMemberWeight(_ context.Context, input groups.UpdateMemberWeightInput, event audit.Event) (groups.UpdateMemberWeightResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := input.UpdatedBy + "\x00device_group.member_weight_update\x00" + input.GroupID + "\x00" + input.NodeID + "\x00" + input.IdempotencyKey
	var replay groups.UpdateMemberWeightResult
	if ok, err := s.replayBusinessLocked(key, input.RequestSHA256, &replay); ok || err != nil {
		replay.Replayed = ok
		return replay, err
	}
	members, exists := s.membersByGroup[input.GroupID]
	if !exists {
		return groups.UpdateMemberWeightResult{}, faults.ErrNotFound
	}
	previous, exists := members[input.NodeID]
	if !exists || previous.RetiredAt != nil {
		return groups.UpdateMemberWeightResult{}, faults.ErrNotFound
	}
	if !previous.UpdatedAt.Equal(input.ExpectedUpdatedAt) {
		return groups.UpdateMemberWeightResult{}, fmt.Errorf("%w: device group member changed; reload before saving", faults.ErrConflict)
	}
	result := groups.UpdateMemberWeightResult{Member: cloneGroupMember(previous), Assignments: []generations.NodeConfigGeneration{}}
	if previous.Weight != input.Weight {
		updated := previous
		updated.Weight = input.Weight
		updated.UpdatedAt = input.UpdatedAt
		if !updated.UpdatedAt.After(previous.UpdatedAt) {
			updated.UpdatedAt = previous.UpdatedAt.Add(time.Nanosecond)
		}
		members[input.NodeID] = updated
		assignment, created, err := s.compileNodeConfigLocked(input.NodeID, updated.UpdatedAt)
		if err != nil {
			members[input.NodeID] = previous
			return groups.UpdateMemberWeightResult{}, err
		}
		if created {
			result.Assignments = append(result.Assignments, assignment)
		}
		s.syncGroupMemberToEndpointPoolsLocked(updated)
		group := s.deviceGroups[input.GroupID]
		group.UpdatedAt = updated.UpdatedAt
		s.deviceGroups[input.GroupID] = group
		result.Member = cloneGroupMember(updated)
	}
	if err := s.recordBusinessLocked(key, input.RequestSHA256, input.GroupID+":"+input.NodeID, result); err != nil {
		return groups.UpdateMemberWeightResult{}, err
	}
	s.appendAuditLocked(event)
	return result, nil
}

// upsertGroupMemberLocked is shared by the administrator member endpoint and
// group-scoped node enrollment. Callers must hold s.mu.
func (s *Store) upsertGroupMemberLocked(member groups.Member) (groups.Member, []generations.NodeConfigGeneration, error) {
	return s.upsertGroupMemberLockedWithCompilePolicy(member, false)
}

// Enrollment should establish identity and membership even when an unrelated
// historical rule cannot currently compile. The node remains visible and can
// receive a repaired bundle after the rule is fixed.
func (s *Store) upsertGroupMemberForEnrollmentLocked(member groups.Member) (groups.Member, error) {
	stored, _, err := s.upsertGroupMemberLockedWithCompilePolicy(member, true)
	return stored, err
}

func (s *Store) upsertGroupMemberLockedWithCompilePolicy(member groups.Member, tolerateCompileFailure bool) (groups.Member, []generations.NodeConfigGeneration, error) {
	if _, exists := s.deviceGroups[member.GroupID]; !exists {
		return groups.Member{}, nil, faults.ErrNotFound
	}
	if _, exists := s.nodes[member.NodeID]; !exists {
		return groups.Member{}, nil, faults.ErrNotFound
	}
	if err := s.validateForwardingMemberListenersLocked(member); err != nil {
		return groups.Member{}, nil, err
	}
	existing, exists := s.membersByGroup[member.GroupID][member.NodeID]
	changed := !exists || existing.RetiredAt != nil || existing.Weight != member.Weight || existing.Priority != member.Priority || existing.DialHost != member.DialHost
	if exists {
		member.CreatedAt = existing.CreatedAt
	}
	s.membersByGroup[member.GroupID][member.NodeID] = member
	assignments := make([]generations.NodeConfigGeneration, 0, 1)
	if changed {
		assignment, created, err := s.compileNodeConfigLocked(member.NodeID, member.UpdatedAt)
		if err != nil {
			if tolerateCompileFailure {
				s.syncGroupMemberToEndpointPoolsLocked(member)
				group := s.deviceGroups[member.GroupID]
				group.UpdatedAt = member.UpdatedAt
				s.deviceGroups[member.GroupID] = group
				return cloneGroupMember(member), nil, nil
			}
			if exists {
				s.membersByGroup[member.GroupID][member.NodeID] = existing
			} else {
				delete(s.membersByGroup[member.GroupID], member.NodeID)
			}
			return groups.Member{}, nil, err
		}
		if created {
			assignments = append(assignments, assignment)
		}
	}
	s.syncGroupMemberToEndpointPoolsLocked(member)
	if changed {
		group := s.deviceGroups[member.GroupID]
		group.UpdatedAt = member.UpdatedAt
		s.deviceGroups[member.GroupID] = group
	}
	return member, assignments, nil
}

func (s *Store) RetireGroupMember(_ context.Context, groupID, nodeID string, retiredAt time.Time, event audit.Event) (groups.RetireMemberResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	members, groupExists := s.membersByGroup[groupID]
	if !groupExists {
		return groups.RetireMemberResult{}, faults.ErrNotFound
	}
	previous, exists := members[nodeID]
	if !exists {
		return groups.RetireMemberResult{}, faults.ErrNotFound
	}
	if previous.RetiredAt != nil {
		return groups.RetireMemberResult{Member: cloneGroupMember(previous), Assignments: []generations.NodeConfigGeneration{}, Replayed: true}, nil
	}
	retiredAt = retiredAt.UTC()
	member := previous
	member.RetiredAt = &retiredAt
	member.UpdatedAt = retiredAt
	members[nodeID] = member
	assignment, created, err := s.compileNodeConfigLocked(nodeID, retiredAt)
	if err != nil {
		members[nodeID] = previous
		return groups.RetireMemberResult{}, err
	}
	assignments := make([]generations.NodeConfigGeneration, 0, 1)
	if created {
		assignments = append(assignments, assignment)
	}
	s.syncGroupMemberToEndpointPoolsLocked(member)
	group := s.deviceGroups[groupID]
	group.UpdatedAt = retiredAt
	s.deviceGroups[groupID] = group
	s.appendAuditLocked(event)
	return groups.RetireMemberResult{Member: cloneGroupMember(member), Assignments: assignments}, nil
}
