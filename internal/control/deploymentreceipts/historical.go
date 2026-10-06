package deploymentreceipts

import (
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// VerifiedHistoricalBundle checks immutable bundle content and real-engine
// commit/verify receipts from the same attempt for this authenticated node.
func VerifiedHistoricalBundle(input HistoricalInput, nodeID string, generation int64, engine agentv1.Engine, bundle agentv1.ConfigurationBundle) bool {
	config := &input.Config
	if input.Invalidated || input.AttemptID == "" || config.NodeID != nodeID || config.Generation != generation ||
		config.Engine != agentv1.EngineNodeBundle || !validBundleHash(config) || bundle.SchemaVersion != 1 {
		return false
	}
	for _, phase := range []agentv1.ApplyPhase{agentv1.ApplyPhaseCommit, agentv1.ApplyPhaseVerify} {
		result, ok := input.ApplyResults[phase]
		if !ok || result.NodeID != nodeID || result.Generation != generation || result.ConfigSHA256 != config.ConfigSHA256 ||
			result.AttemptID != input.AttemptID || result.Phase != phase || result.Status != agentv1.ApplyStatusSucceeded ||
			!attestsEngine(result.EngineMode, engine, bundle) {
			return false
		}
	}
	return true
}
