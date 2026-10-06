package memoryrepo

import (
	"context"

	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

var _ deploymentreceipts.Repository = (*Store)(nil)

func (s *Store) RuleNodeDeploymentInput(_ context.Context, ruleID, nodeID string) (deploymentreceipts.Input, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ruleNodeDeploymentInputLocked(ruleID, nodeID)
}

func (s *Store) ruleNodeDeploymentInputLocked(ruleID, nodeID string) (deploymentreceipts.Input, error) {
	rule, ruleExists := s.forwardRules[ruleID]
	node, nodeExists := s.nodes[nodeID]
	if !ruleExists || !nodeExists {
		return deploymentreceipts.Input{}, faults.ErrNotFound
	}
	member, memberExists := s.membersByGroup[rule.EntryGroupID][nodeID]
	input := deploymentreceipts.Input{
		Rule:         cloneForwardingRule(s.forwardingBaseViewLocked(rule)),
		Node:         cloneNode(node),
		ActiveMember: memberExists && member.RetiredAt == nil,
		ApplyResults: make(map[agentv1.ApplyPhase]generations.ApplyResult),
	}
	if network, ok := s.groupNetworks[rule.EntryGroupID]; ok {
		input.EntryNetwork = &network
	}
	if network, ok := s.groupNetworks[rule.ExitGroupID]; ok {
		input.ExitNetwork = &network
	}
	if node.DesiredGeneration > 0 {
		attempt := s.applyAttemptsByNode[nodeID][node.DesiredGeneration]
		input.CurrentAttemptID = attempt.CurrentID
		input.AttemptInvalidated = attempt.Invalidated
		if config, exists := s.nodeConfigsByNode[nodeID][node.DesiredGeneration]; exists {
			copy := cloneNodeConfig(config)
			input.CurrentConfig = &copy
		}
		for phase, result := range s.applyResultsByNode[nodeID][node.DesiredGeneration] {
			input.ApplyResults[agentv1.ApplyPhase(phase)] = result
		}
	}
	return input, nil
}
