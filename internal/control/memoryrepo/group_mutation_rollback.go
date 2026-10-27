package memoryrepo

import (
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
)

// groupMemberMutationSnapshot captures every in-memory projection touched by
// a member mutation. PostgreSQL gets rollback from its transaction boundary;
// the memory repository needs the same guarantee for tests and volatile mode.
type groupMemberMutationSnapshot struct {
	groupID string
	nodeID  string

	group          groups.DeviceGroup
	groupExists    bool
	member         groups.Member
	memberExists   bool
	relatedNodes   map[string]nodes.Node
	relatedConfigs map[string]map[int64]generations.NodeConfigGeneration
	node           nodes.Node
	nodeExists     bool
	configs        map[int64]generations.NodeConfigGeneration
	configsExist   bool

	pools         map[string]endpoints.EndpointPool
	poolMembers   map[string]map[string]endpoints.EndpointPoolMember
	materials     map[string]vlessruntime.Material
	auditEventLen int
}

func (s *Store) captureGroupMemberMutationLocked(groupID, nodeID string) groupMemberMutationSnapshot {
	snapshot := groupMemberMutationSnapshot{
		groupID:       groupID,
		nodeID:        nodeID,
		auditEventLen: len(s.auditEvents),
	}
	snapshot.relatedNodes = make(map[string]nodes.Node)
	snapshot.materials = make(map[string]vlessruntime.Material)
	for id, material := range s.vlessRuntimeMaterials {
		if material.NodeID == nodeID {
			snapshot.materials[id] = cloneVLESSRuntimeMaterial(material)
		}
	}
	snapshot.relatedConfigs = make(map[string]map[int64]generations.NodeConfigGeneration)
	relatedNodeIDs := s.memberDependentNodeIDsLocked([]string{groupID}, nodeID)
	if nodeID == "" {
		// Group-wide policy mutations still affect all group projections.
		for _, id := range s.reliableUDPDependentGroupsLocked([]string{groupID}) {
			for n := range s.membersByGroup[id] {
				relatedNodeIDs[n] = struct{}{}
			}
		}
	}
	for n := range relatedNodeIDs {
		snapshot.relatedNodes[n] = cloneNode(s.nodes[n])
		if configs := s.nodeConfigsByNode[n]; configs != nil {
			copyConfigs := make(map[int64]generations.NodeConfigGeneration, len(configs))
			for gen, config := range configs {
				copyConfigs[gen] = cloneNodeConfig(config)
			}
			snapshot.relatedConfigs[n] = copyConfigs
		}
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
	for id, material := range s.vlessRuntimeMaterials {
		if material.NodeID == snapshot.nodeID {
			delete(s.vlessRuntimeMaterials, id)
		}
	}
	for id, material := range snapshot.materials {
		s.vlessRuntimeMaterials[id] = material
	}
	for id, node := range snapshot.relatedNodes {
		s.nodes[id] = node
		if configs := snapshot.relatedConfigs[id]; configs != nil {
			s.nodeConfigsByNode[id] = configs
		} else {
			delete(s.nodeConfigsByNode, id)
		}
	}
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
