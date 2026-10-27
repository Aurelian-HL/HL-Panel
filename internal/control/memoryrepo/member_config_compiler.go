package memoryrepo

import (
	"time"

	"github.com/hongle/hl-panel/internal/control/generations"
)

// A membership change does not change the other members' business rules.
// Only reliable-UDP entries embed another member's exit address. Keep that
// dependency directional: adding an entry must not recompile its exit peers
// or every other node in the entry group.
func (s *Store) memberDependentNodeIDsLocked(groupIDs []string, changedNodeID string) map[string]struct{} {
	changedGroups := make(map[string]struct{}, len(groupIDs))
	for _, groupID := range groupIDs {
		changedGroups[groupID] = struct{}{}
	}
	nodeIDs := make(map[string]struct{})
	for _, rule := range s.forwardRules {
		if !rule.UsesReliableUDPTunnel() {
			continue
		}
		if _, changed := changedGroups[rule.ExitGroupID]; !changed {
			continue
		}
		for nodeID, member := range s.membersByGroup[rule.EntryGroupID] {
			if nodeID != changedNodeID && member.RetiredAt == nil {
				nodeIDs[nodeID] = struct{}{}
			}
		}
	}
	return nodeIDs
}

// The changed machine receives its complete projection from the current rules.
// Its independent peers keep their existing projections. UDP entries whose
// exit membership changed are updated in the same rollback-protected batch.
func (s *Store) recompileMemberConfigsLocked(nodeID string, groupIDs []string, now time.Time) (generations.NodeConfigGeneration, bool, error) {
	previousGeneration := s.nodes[nodeID].DesiredGeneration
	nodeIDs := s.memberDependentNodeIDsLocked(groupIDs, nodeID)
	nodeIDs[nodeID] = struct{}{}
	if err := s.recompileNodesLocked(nodeIDs, now); err != nil {
		return generations.NodeConfigGeneration{}, false, err
	}
	node := s.nodes[nodeID]
	if node.DesiredGeneration == previousGeneration {
		return generations.NodeConfigGeneration{}, false, nil
	}
	return cloneNodeConfig(s.nodeConfigsByNode[nodeID][node.DesiredGeneration]), true, nil
}
