package memoryrepo

import (
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

// groupMemberMutationSnapshot captures every in-memory projection touched by
// a member mutation. PostgreSQL gets rollback from its transaction boundary;
// the memory repository needs the same guarantee for tests and volatile mode.
type groupMemberMutationSnapshot struct {
	groupID string
	nodeID  string

	group        groups.DeviceGroup
	groupExists  bool
	member       groups.Member
	memberExists bool
	node         nodes.Node
	nodeExists   bool
	configs      map[int64]generations.NodeConfigGeneration
	configsExist bool

	pools         map[string]endpoints.EndpointPool
	poolMembers   map[string]map[string]endpoints.EndpointPoolMember
	auditEventLen int
}

func (s *Store) captureGroupMemberMutationLocked(groupID, nodeID string) groupMemberMutationSnapshot {
	snapshot := groupMemberMutationSnapshot{
		groupID:       groupID,
		nodeID:        nodeID,
		auditEventLen: len(s.auditEvents),
	}
	snapshot.group, snapshot.groupExists = s.deviceGroups[groupID]
	if members, exists := s.membersByGroup[groupID]; exists {
		snapshot.member, snapshot.memberExists = members[nodeID]
	}
	snapshot.node, snapshot.nodeExists = s.nodes[nodeID]
	if configs, exists := s.nodeConfigsByNode[nodeID]; exists {
		snapshot.configsExist = true
		snapshot.configs = make(map[int64]generations.NodeConfigGeneration, len(configs))
		for generation, config := range configs {
			snapshot.configs[generation] = cloneNodeConfig(config)
		}
	}
	snapshot.pools = make(map[string]endpoints.EndpointPool)
	snapshot.poolMembers = make(map[string]map[string]endpoints.EndpointPoolMember)
	for poolID, pool := range s.endpointPools {
		if pool.GroupID != groupID {
			continue
		}
		snapshot.pools[poolID] = cloneEndpointPool(pool)
		members := s.endpointMembers[poolID]
		copyMembers := make(map[string]endpoints.EndpointPoolMember, len(members))
		for memberID, member := range members {
			copyMembers[memberID] = cloneEndpointMember(member)
		}
		snapshot.poolMembers[poolID] = copyMembers
	}
	return snapshot
}

func (s *Store) restoreGroupMemberMutationLocked(snapshot groupMemberMutationSnapshot) {
	if snapshot.groupExists {
		s.deviceGroups[snapshot.groupID] = snapshot.group
	} else {
		delete(s.deviceGroups, snapshot.groupID)
	}
	members := s.membersByGroup[snapshot.groupID]
	if snapshot.memberExists {
		members[snapshot.nodeID] = snapshot.member
	} else {
		delete(members, snapshot.nodeID)
	}
	if snapshot.nodeExists {
		s.nodes[snapshot.nodeID] = cloneNode(snapshot.node)
	} else {
		delete(s.nodes, snapshot.nodeID)
	}
	if snapshot.configsExist {
		s.nodeConfigsByNode[snapshot.nodeID] = snapshot.configs
	} else {
		delete(s.nodeConfigsByNode, snapshot.nodeID)
	}
	for poolID, pool := range snapshot.pools {
		s.endpointPools[poolID] = pool
		s.endpointMembers[poolID] = snapshot.poolMembers[poolID]
	}
	if len(s.auditEvents) > snapshot.auditEventLen {
		s.auditEvents = s.auditEvents[:snapshot.auditEventLen]
	}
}
