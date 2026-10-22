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
	input, err := s.ruleNodeDeploymentInputBorrowedLocked(ruleID, nodeID)
	if err == nil && input.CurrentConfig != nil {
		copy := cloneNodeConfig(*input.CurrentConfig)
		input.CurrentConfig = &copy
	}
	if err == nil && input.AppliedConfig != nil {
		copy := cloneNodeConfig(*input.AppliedConfig)
		input.AppliedConfig = &copy
	}
	if err == nil {
		input.Node = cloneNode(input.Node)
		if input.EntryNetwork != nil {
			copy := cloneGroupNetwork(*input.EntryNetwork)
			input.EntryNetwork = &copy
		}
		if input.ExitNetwork != nil {
			copy := cloneGroupNetwork(*input.ExitNetwork)
			input.ExitNetwork = &copy
		}
	}
	return input, err
}

// Borrow configuration bytes only for synchronous evaluation under s.mu.
// Public repository results must use ruleNodeDeploymentInputLocked instead.
func (s *Store) ruleNodeDeploymentInputBorrowedLocked(ruleID, nodeID string) (deploymentreceipts.Input, error) {
	rule, ruleExists := s.forwardRules[ruleID]
	node, nodeExists := s.nodes[nodeID]
	if !ruleExists || !nodeExists {
		return deploymentreceipts.Input{}, faults.ErrNotFound
	}
	member, memberExists := s.membersByGroup[rule.EntryGroupID][nodeID]
	if rule.UsesReliableUDPTunnel() && (!memberExists || member.RetiredAt != nil) {
		member, memberExists = s.membersByGroup[rule.ExitGroupID][nodeID]
	}
	input := deploymentreceipts.Input{
		Rule:         cloneForwardingRule(s.forwardingBaseViewLocked(rule)),
		Node:         node, // Borrowed under s.mu; exported repository results clone above.
		ActiveMember: memberExists && member.RetiredAt == nil,
		ApplyResults: make(map[agentv1.ApplyPhase]generations.ApplyResult),
	}
	if rule.UsesReliableUDPTunnel() {
		entry, e := s.membersByGroup[rule.EntryGroupID][nodeID]
		exit, x := s.membersByGroup[rule.ExitGroupID][nodeID]
		input.RequireTunnelEntry = e && entry.RetiredAt == nil
		input.RequireTunnelExit = x && exit.RetiredAt == nil
		input.ActiveMember = input.ActiveMember && nodeSupportsReliableUDP(node)
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
			copy := config
			input.CurrentConfig = &copy
		}
		for phase, result := range s.applyResultsByNode[nodeID][node.DesiredGeneration] {
			input.ApplyResults[agentv1.ApplyPhase(phase)] = result
		}
	}
	if node.AppliedGeneration > 0 && node.AppliedGeneration != node.DesiredGeneration {
		attempt := s.applyAttemptsByNode[nodeID][node.AppliedGeneration]
		input.AppliedAttemptID, input.AppliedInvalidated = attempt.CurrentID, attempt.Invalidated
		if config, exists := s.nodeConfigsByNode[nodeID][node.AppliedGeneration]; exists {
			copy := config
			input.AppliedConfig = &copy
		}
		input.AppliedResults = make(map[agentv1.ApplyPhase]generations.ApplyResult)
		for phase, result := range s.applyResultsByNode[nodeID][node.AppliedGeneration] {
			input.AppliedResults[agentv1.ApplyPhase(phase)] = result
		}
	}
	return input, nil
}

func (s *Store) evaluateDeploymentLocked(input deploymentreceipts.Input) deploymentreceipts.Status {
	if s.receiptEvaluator != nil {
		return s.receiptEvaluator.Evaluate(input)
	}
	return deploymentreceipts.Evaluate(input)
}
