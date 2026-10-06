package generations

import "github.com/hongle/hl-panel/internal/protocol/agentv1"

const (
	ForwardingGOSTFragmentPrefix  = "forwarding-gost/"
	ForwardingVLESSFragmentPrefix = "forwarding-vless/"
)

// ForwardingFragmentID gives each rule's engine fragment a stable owner
// inside a complete node bundle. Empty means the engine is unsupported.
func ForwardingFragmentID(engine agentv1.Engine, ruleID string) string {
	if ruleID == "" {
		return ""
	}
	switch engine {
	case agentv1.EngineGOST:
		return ForwardingGOSTFragmentPrefix + ruleID
	case agentv1.EngineXray:
		return ForwardingVLESSFragmentPrefix + ruleID
	default:
		return ""
	}
}
