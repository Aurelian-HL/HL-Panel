package deploymentreceipts

import (
	"context"
	"encoding/json"

	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type Service struct{ repository Repository }

func NewService(repository Repository) *Service { return &Service{repository: repository} }

func (s *Service) ForRuleNode(ctx context.Context, ruleID, nodeID string) (Status, error) {
	input, err := s.repository.RuleNodeDeploymentInput(ctx, ruleID, nodeID)
	if err != nil {
		return Status{}, err
	}
	return Evaluate(input), nil
}

// Evaluate proves only that a specific rule fragment was present in a current
// node bundle and that an explicit real-engine verify receipt matched it. It
// does not establish reachability, client authentication, or protocol health.
func Evaluate(input Input) Status {
	status := Status{RuleID: input.Rule.ID, NodeID: input.Node.ID, Reason: ReasonConfigMissing}
	if input.Rule.Paused ||
		(input.Rule.Status != forwarding.StatusPendingActivation && input.Rule.Status != forwarding.StatusActive) ||
		input.Rule.IngressReadiness() != forwarding.IngressReady {
		status.Reason = ReasonRuleInactive
		return status
	}
	if !input.ActiveMember || input.Node.ID == "" || input.Rule.EntryGroupID == "" {
		status.Reason = ReasonNodeNotMember
		return status
	}
	config := input.CurrentConfig
	if config == nil || config.Generation <= 0 || config.NodeID != input.Node.ID ||
		config.Generation != input.Node.DesiredGeneration || config.Engine != agentv1.EngineNodeBundle {
		return status
	}
	status.NodeConfigGeneration = config.Generation
	status.ConfigSHA256 = config.ConfigSHA256
	if !validBundleHash(config) {
		status.Reason = ReasonConfigInvalid
		return status
	}
	var bundle agentv1.ConfigurationBundle
	if err := json.Unmarshal(config.Config, &bundle); err != nil || bundle.SchemaVersion != 1 {
		status.Reason = ReasonConfigInvalid
		return status
	}
	fragmentID, engine, ok := expectedFragment(input.Rule)
	if !ok {
		status.Reason = ReasonFragmentMissing
		return status
	}
	var count int
	for _, fragment := range bundle.Fragments {
		if fragment.GroupID != fragmentID {
			continue
		}
		count++
		if fragment.Engine != engine || int64(fragment.GroupRevision) != input.Rule.Revision ||
			len(fragment.Config) == 0 || !json.Valid(fragment.Config) {
			status.Reason = ReasonFragmentMismatch
			return status
		}
	}
	if count == 0 {
		status.Reason = ReasonFragmentMissing
		return status
	}
	if count != 1 {
		status.Reason = ReasonFragmentMismatch
		return status
	}
	status.FragmentEmitted = true
	status.Engine = engine
	status.ApplyAttemptID = input.CurrentAttemptID
	if input.AttemptInvalidated || input.Node.AppliedGeneration != config.Generation ||
		!matchingSuccessfulReceipt(input, agentv1.ApplyPhaseCommit, config) ||
		!matchingSuccessfulReceipt(input, agentv1.ApplyPhaseVerify, config) {
		status.Reason = ReasonApplyPending
		return status
	}
	// Old agent reports lack engine mode. They are deliberately insufficient,
	// even if they advanced the node's applied generation in old snapshots.
	if input.CurrentAttemptID == "" ||
		!attestsEngine(input.ApplyResults[agentv1.ApplyPhaseCommit].EngineMode, engine, bundle) ||
		!attestsEngine(input.ApplyResults[agentv1.ApplyPhaseVerify].EngineMode, engine, bundle) {
		status.Reason = ReasonEngineUnverified
		return status
	}
	status.ReceiptVerified = true
	status.Reason = ReasonReceiptVerified
	return status
}

func attestsEngine(mode string, expected agentv1.Engine, bundle agentv1.ConfigurationBundle) bool {
	var hasXray, hasGOST bool
	for _, fragment := range bundle.Fragments {
		hasXray = hasXray || fragment.Engine == agentv1.EngineXray
		hasGOST = hasGOST || fragment.Engine == agentv1.EngineGOST
	}
	if hasXray && hasGOST {
		return mode == "mixed" && (expected == agentv1.EngineXray || expected == agentv1.EngineGOST)
	}
	return mode == string(expected) &&
		(expected == agentv1.EngineXray && hasXray || expected == agentv1.EngineGOST && hasGOST)
}

func matchingSuccessfulReceipt(input Input, phase agentv1.ApplyPhase, config *generations.NodeConfigGeneration) bool {
	result, exists := input.ApplyResults[phase]
	return exists && result.NodeID == input.Node.ID && result.Generation == config.Generation &&
		result.ConfigSHA256 == config.ConfigSHA256 && result.AttemptID == input.CurrentAttemptID &&
		result.Phase == phase && result.Status == agentv1.ApplyStatusSucceeded
}

func validBundleHash(config *generations.NodeConfigGeneration) bool {
	return len(config.ConfigSHA256) == 64 && generations.SHA256Hex(config.Config) == config.ConfigSHA256
}

func expectedFragment(rule forwarding.Rule) (string, agentv1.Engine, bool) {
	switch rule.EffectiveIngressProtocol() {
	case forwarding.IngressVLESSReality:
		return generations.ForwardingFragmentID(agentv1.EngineXray, rule.ID), agentv1.EngineXray, true
	case forwarding.IngressTCP:
		return generations.ForwardingFragmentID(agentv1.EngineGOST, rule.ID), agentv1.EngineGOST, true
	default:
		return "", "", false
	}
}
