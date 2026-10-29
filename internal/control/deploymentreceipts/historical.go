package deploymentreceipts

import (
	"encoding/json"

	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// VerifiedHistoricalBundle checks immutable bundle content and real-engine
// commit/verify receipts from the same attempt for this authenticated node.
func VerifiedHistoricalBundle(input HistoricalInput, nodeID string, generation int64, engine agentv1.Engine, bundle agentv1.ConfigurationBundle) bool {
	return verifiedHistoricalReceipts(input, nodeID, generation, bundle, func(mode string) bool {
		return attestsEngine(mode, engine, bundle)
	})
}

// VerifiedHistoricalTransition attests the configuration that removed a rule.
// This proves an accounting transition only, never current rule availability.
// An empty bundle may retain idle engines for existing connections. Require
// explicit live-engine receipts even there; empty legacy/dry-run modes cannot
// prove that a real engine applied the removal.
func VerifiedHistoricalTransition(input HistoricalInput, nodeID string, generation int64, bundle agentv1.ConfigurationBundle) bool {
	if len(bundle.Fragments) == 0 {
		return verifiedHistoricalReceipts(input, nodeID, generation, bundle, func(mode string) bool {
			return mode == "xray" || mode == "gost" || mode == "mixed"
		}) && input.ApplyResults[agentv1.ApplyPhaseCommit].EngineMode == input.ApplyResults[agentv1.ApplyPhaseVerify].EngineMode
	}
	for _, fragment := range bundle.Fragments {
		if fragment.Engine != agentv1.EngineXray && fragment.Engine != agentv1.EngineGOST {
			return false
		}
	}
	return VerifiedHistoricalBundle(input, nodeID, generation, bundle.Fragments[0].Engine, bundle)
}

func verifiedHistoricalReceipts(input HistoricalInput, nodeID string, generation int64, bundle agentv1.ConfigurationBundle, attests func(string) bool) bool {
	config := &input.Config
	if input.Invalidated || input.AttemptID == "" || config.NodeID != nodeID || config.Generation != generation ||
		config.Engine != agentv1.EngineNodeBundle || !validBundleHash(config) || bundle.SchemaVersion != 1 {
		return false
	}
	for _, phase := range []agentv1.ApplyPhase{agentv1.ApplyPhaseCommit, agentv1.ApplyPhaseVerify} {
		result, ok := input.ApplyResults[phase]
		if !ok || result.NodeID != nodeID || result.Generation != generation || result.ConfigSHA256 != config.ConfigSHA256 ||
			result.AttemptID != input.AttemptID || result.Phase != phase || result.Status != agentv1.ApplyStatusSucceeded ||
			!attests(result.EngineMode) {
			return false
		}
	}
	return true
}

// ContainsUsageRule selects history by fragment identity only. Validation of
// metadata, counter tags and receipts happens after selecting the latest match,
// so malformed or invalidated matches cannot be silently skipped.
func ContainsUsageRule(input HistoricalInput, ruleID string) (bool, error) {
	var bundle struct {
		Fragments []struct {
			GroupID string `json:"group_id"`
		} `json:"fragments"`
	}
	if err := json.Unmarshal(input.Config.Config, &bundle); err != nil {
		return false, err
	}
	for _, f := range bundle.Fragments {
		if f.GroupID == generations.ForwardingFragmentID(agentv1.EngineXray, ruleID) ||
			f.GroupID == generations.ForwardingFragmentID(agentv1.EngineGOST, ruleID) {
			return true, nil
		}
	}
	return false, nil
}
