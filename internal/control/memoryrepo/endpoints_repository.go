package memoryrepo

import (
	"context"
	"fmt"
	"sort"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
)

func (s *Store) CreateEndpointPool(_ context.Context, input endpoints.CreatePoolInput, event audit.Event) (endpoints.EndpointPool, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := input.CreatedBy + "\x00" + input.IdempotencyKey
	if poolID, replay := s.endpointCreateKeys[key]; replay {
		if s.endpointCreateHashes[key] != input.RequestSHA256 {
			return endpoints.EndpointPool{}, false, faults.ErrIdempotencyConflict
		}
		pool, exists := s.endpointPools[poolID]
		if !exists {
			return endpoints.EndpointPool{}, false, fmt.Errorf("%w: endpoint pool was deleted", faults.ErrConflict)
		}
		pool.MemberCount = len(s.endpointMembers[poolID])
		pool.HealthyCandidateCount = 0
		return cloneEndpointPool(pool), true, nil
	}
	if _, exists := s.deviceGroups[input.GroupID]; !exists {
		return endpoints.EndpointPool{}, false, faults.ErrNotFound
	}
	if input.CreatedBy == "" {
		return endpoints.EndpointPool{}, false, faults.ErrValidation
	}
	if input.RuleID != "" {
		rule, exists := s.forwardRules[input.RuleID]
		if !exists || !rule.OwnedByAdministrator(input.CreatedBy) {
			return endpoints.EndpointPool{}, false, faults.ErrNotFound
		}
		network, configured := s.groupNetworks[input.GroupID]
		if !configured || network.ConnectHost != input.Hostname || !network.ContainsPort(input.Port) {
			return endpoints.EndpointPool{}, false, fmt.Errorf("%w: bound endpoint must use its entry group's configured address and allowed listener port", faults.ErrValidation)
		}
		if rule.EntryGroupID != input.GroupID || rule.ListenPort != input.Port ||
			(input.Protocol == "vless" && rule.EffectiveIngressProtocol() != forwarding.IngressVLESSReality) ||
			(input.Protocol == "tcp" && rule.EffectiveIngressProtocol() != forwarding.IngressTCP) ||
			(input.Protocol == "socks5" && rule.EffectiveIngressProtocol() != forwarding.IngressSOCKS5) {
			return endpoints.EndpointPool{}, false, fmt.Errorf("%w: endpoint pool rule must match device group, protocol and listener port", faults.ErrValidation)
		}
	}
	for _, pool := range s.endpointPools {
		if input.RuleID != "" && pool.RuleID == input.RuleID {
			// A forwarding rule owns one customer-visible stable endpoint. The
			// protocol-health publisher is keyed by rule and node, so allowing
			// multiple pools for the same rule would make a valid observation
			// land in an arbitrary pool and mix device-group membership.
			return endpoints.EndpointPool{}, false, fmt.Errorf("%w: forwarding rule already has a stable endpoint", faults.ErrConflict)
		}
		if pool.Name == input.Name && pool.GroupID == input.GroupID {
			return endpoints.EndpointPool{}, false, fmt.Errorf("%w: endpoint pool name already exists in group", faults.ErrConflict)
		}
		if pool.Protocol == input.Protocol && pool.Hostname == input.Hostname && pool.Port == input.Port {
			return endpoints.EndpointPool{}, false, fmt.Errorf("%w: stable endpoint is already allocated", faults.ErrConflict)
		}
	}
	pool := endpoints.EndpointPool{
		ID: input.ID, OwnerID: input.CreatedBy, Name: input.Name, GroupID: input.GroupID, RuleID: input.RuleID, Mode: input.Mode,
		Protocol: input.Protocol, Hostname: input.Hostname, Port: input.Port,
		SelectionPolicy: input.SelectionPolicy, CreatedAt: input.CreatedAt, UpdatedAt: input.CreatedAt,
	}
	members := make(map[string]endpoints.EndpointPoolMember)
	for nodeID, groupMember := range s.membersByGroup[input.GroupID] {
		if groupMember.RetiredAt != nil {
			continue
		}
		members[nodeID] = endpoints.EndpointPoolMember{
			PoolID: input.ID, GroupID: input.GroupID, NodeID: nodeID,
			DialHost: groupMember.DialHost,
			Weight:   groupMember.Weight, Priority: groupMember.Priority,
			State: endpoints.CandidateEligible, UpdatedAt: input.CreatedAt,
		}
	}
	pool.MemberCount = len(members)
	s.endpointPools[pool.ID] = pool
	s.endpointMembers[pool.ID] = members
	s.endpointCreateKeys[key] = pool.ID
	s.endpointCreateHashes[key] = input.RequestSHA256
	s.appendAuditLocked(event)
	return cloneEndpointPool(pool), false, nil
}

func (s *Store) DeleteEndpointPool(_ context.Context, input endpoints.DeletePoolInput, event audit.Event) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := input.DeletedBy + "\x00endpoint_pool.delete\x00" + input.PoolID + "\x00" + input.IdempotencyKey
	var result bool
	if replayed, err := s.replayBusinessLocked(key, input.RequestSHA256, &result); replayed || err != nil {
		return replayed, err
	}
	pool, exists := s.endpointPools[input.PoolID]
	if !exists || pool.OwnerID != input.DeletedBy {
		return false, faults.ErrNotFound
	}
	for _, record := range s.vlessBindings {
		// Revoked identities are retained as historical audit records and no
		// longer reference live runtime state. Only an active identity keeps a
		// stable endpoint pool in use.
		if record.Binding.EndpointPoolID == input.PoolID && record.Binding.State == vlessidentity.StateActive {
			return false, fmt.Errorf("%w: endpoint pool is referenced by a customer identity", faults.ErrConflict)
		}
	}
	for _, material := range s.vlessRuntimeMaterials {
		if material.EndpointPoolID == input.PoolID && material.State == vlessruntime.StateActive {
			return false, fmt.Errorf("%w: endpoint pool has active runtime material", faults.ErrConflict)
		}
	}
	for _, member := range s.endpointMembers[input.PoolID] {
		if member.ActiveConnections > 0 {
			return false, fmt.Errorf("%w: endpoint pool has active connections", faults.ErrConflict)
		}
	}
	if err := s.recordBusinessLocked(key, input.RequestSHA256, input.PoolID, true); err != nil {
		return false, err
	}
	delete(s.endpointPools, input.PoolID)
	delete(s.endpointMembers, input.PoolID)
	s.appendAuditLocked(event)
	return false, nil
}

func (s *Store) ListEndpointPools(_ context.Context) ([]endpoints.EndpointPool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]endpoints.EndpointPool, 0, len(s.endpointPools))
	for _, stored := range s.endpointPools {
		pool := cloneEndpointPool(stored)
		pool.MemberCount = len(s.endpointMembers[pool.ID])
		pool.HealthyCandidateCount = 0
		items = append(items, pool)
	}
	sort.Slice(items, func(left, right int) bool {
		if items[left].Name == items[right].Name {
			return items[left].ID < items[right].ID
		}
		return items[left].Name < items[right].Name
	})
	return items, nil
}

func (s *Store) EndpointPool(_ context.Context, poolID string) (endpoints.EndpointPool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pool, exists := s.endpointPools[poolID]
	if !exists {
		return endpoints.EndpointPool{}, faults.ErrNotFound
	}
	pool = cloneEndpointPool(pool)
	pool.MemberCount = len(s.endpointMembers[poolID])
	pool.HealthyCandidateCount = 0
	return pool, nil
}

func (s *Store) EndpointPoolMembers(_ context.Context, poolID string) ([]endpoints.EndpointPoolMember, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, exists := s.endpointPools[poolID]; !exists {
		return nil, faults.ErrNotFound
	}
	items := make([]endpoints.EndpointPoolMember, 0, len(s.endpointMembers[poolID]))
	for _, member := range s.endpointMembers[poolID] {
		items = append(items, cloneEndpointMember(member))
	}
	sort.Slice(items, func(left, right int) bool { return items[left].NodeID < items[right].NodeID })
	return items, nil
}

func (s *Store) AddEndpointPoolMember(_ context.Context, input endpoints.AddMemberInput, event audit.Event) (endpoints.EndpointPoolMember, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pool, exists := s.endpointPools[input.PoolID]
	if !exists {
		return endpoints.EndpointPoolMember{}, false, faults.ErrNotFound
	}
	if pool.OwnerID != input.CreatedBy {
		return endpoints.EndpointPoolMember{}, false, faults.ErrNotFound
	}
	if input.Weight < 0 || input.Weight > 1000 || input.Priority < 0 || input.Priority > 1000 {
		return endpoints.EndpointPoolMember{}, false, fmt.Errorf("%w: endpoint member weight/priority is outside the allowed range", faults.ErrValidation)
	}
	groupMember, belongs := s.membersByGroup[pool.GroupID][input.NodeID]
	if !belongs || groupMember.RetiredAt != nil {
		return endpoints.EndpointPoolMember{}, false, fmt.Errorf("%w: node is not an active member of the endpoint pool device group", faults.ErrConflict)
	}
	if input.Weight != groupMember.Weight || input.Priority != groupMember.Priority {
		return endpoints.EndpointPoolMember{}, false, fmt.Errorf("%w: endpoint candidate weight and priority are managed by the device group", faults.ErrConflict)
	}
	key := input.CreatedBy + "\x00" + input.PoolID + "\x00" + input.IdempotencyKey
	if memberRef, replay := s.endpointMemberKeys[key]; replay {
		if s.endpointMemberHashes[key] != input.RequestSHA256 {
			return endpoints.EndpointPoolMember{}, false, faults.ErrIdempotencyConflict
		}
		member, projected := s.endpointMembers[input.PoolID][memberRef]
		if !projected || memberRef != input.NodeID {
			return endpoints.EndpointPoolMember{}, false, fmt.Errorf("%w: endpoint candidate projection is no longer active", faults.ErrConflict)
		}
		return cloneEndpointMember(member), true, nil
	}
	member, projected := s.endpointMembers[input.PoolID][input.NodeID]
	if !projected {
		member = endpoints.EndpointPoolMember{
			PoolID: input.PoolID, GroupID: pool.GroupID, NodeID: input.NodeID,
			DialHost: groupMember.DialHost,
			State:    endpoints.CandidateEligible,
		}
	}
	member.Weight = groupMember.Weight
	member.Priority = groupMember.Priority
	member.DialHost = groupMember.DialHost
	if !projected {
		member.UpdatedAt = input.CreatedAt
	}
	s.endpointMembers[input.PoolID][input.NodeID] = member
	s.endpointMemberKeys[key] = input.NodeID
	s.endpointMemberHashes[key] = input.RequestSHA256
	pool.MemberCount = len(s.endpointMembers[input.PoolID])
	if !projected {
		pool.UpdatedAt = input.CreatedAt
	}
	s.endpointPools[pool.ID] = pool
	event.ResourceID = input.PoolID + ":" + input.NodeID
	s.appendAuditLocked(event)
	return cloneEndpointMember(member), false, nil
}

func cloneEndpointPool(pool endpoints.EndpointPool) endpoints.EndpointPool {
	return pool
}

// syncGroupMemberToEndpointPoolsLocked projects the device-group source of
// truth into every endpoint pool bound to that group. Routing state and health
// observations belong to the endpoint lifecycle, so group weight changes must
// not reset draining/quarantine state or turn an unverified member healthy.
func (s *Store) syncGroupMemberToEndpointPoolsLocked(groupMember groups.Member) {
	for poolID, pool := range s.endpointPools {
		if pool.GroupID != groupMember.GroupID {
			continue
		}
		members := s.endpointMembers[poolID]
		member, exists := members[groupMember.NodeID]
		changed := false
		if groupMember.RetiredAt != nil {
			if exists {
				delete(members, groupMember.NodeID)
				changed = true
			}
		} else if !exists {
			members[groupMember.NodeID] = endpoints.EndpointPoolMember{
				PoolID: poolID, GroupID: groupMember.GroupID, NodeID: groupMember.NodeID,
				DialHost: groupMember.DialHost,
				Weight:   groupMember.Weight, Priority: groupMember.Priority,
				State: endpoints.CandidateEligible, UpdatedAt: groupMember.UpdatedAt,
			}
			changed = true
		} else if member.Weight != groupMember.Weight || member.Priority != groupMember.Priority || member.DialHost != groupMember.DialHost {
			if member.DialHost != groupMember.DialHost {
				member.LastHealthAt = nil
				member.LastHealthReason = "address_changed"
			}
			member.Weight = groupMember.Weight
			member.Priority = groupMember.Priority
			member.DialHost = groupMember.DialHost
			member.UpdatedAt = groupMember.UpdatedAt
			members[groupMember.NodeID] = member
			changed = true
		}
		if !changed {
			continue
		}
		s.endpointMembers[poolID] = members
		pool.MemberCount = len(members)
		pool.UpdatedAt = groupMember.UpdatedAt
		s.endpointPools[poolID] = pool
	}
}

func cloneEndpointMember(member endpoints.EndpointPoolMember) endpoints.EndpointPoolMember {
	if member.LastHealthAt != nil {
		copy := *member.LastHealthAt
		member.LastHealthAt = &copy
	}
	return member
}
