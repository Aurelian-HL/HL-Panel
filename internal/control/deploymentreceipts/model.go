// Package deploymentreceipts evaluates rule-scoped evidence from complete
// node configuration generations. A receipt is not a protocol-health lease.
package deploymentreceipts

import (
	"context"

	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type Reason string

const (
	ReasonRuleInactive     Reason = "rule_inactive"
	ReasonNodeNotMember    Reason = "node_not_member"
	ReasonConfigMissing    Reason = "config_missing"
	ReasonConfigInvalid    Reason = "config_invalid"
	ReasonFragmentMissing  Reason = "fragment_missing"
	ReasonFragmentMismatch Reason = "fragment_mismatch"
	ReasonApplyPending     Reason = "apply_pending"
	ReasonEngineUnverified Reason = "engine_unverified"
	ReasonReceiptVerified  Reason = "receipt_verified"
)

// Input is an atomic, read-only snapshot. CurrentConfig and ApplyResults must
// come from the same node state as Node; a historical generation is not enough.
type Input struct {
	Rule               forwarding.Rule                                `json:"-"`
	Node               nodes.Node                                     `json:"-"`
	ActiveMember       bool                                           `json:"-"`
	CurrentConfig      *generations.NodeConfigGeneration              `json:"-"`
	CurrentAttemptID   string                                         `json:"-"`
	AttemptInvalidated bool                                           `json:"-"`
	ApplyResults       map[agentv1.ApplyPhase]generations.ApplyResult `json:"-"`
}

type Status struct {
	RuleID               string         `json:"rule_id"`
	NodeID               string         `json:"node_id"`
	NodeConfigGeneration int64          `json:"node_config_generation"`
	ApplyAttemptID       string         `json:"apply_attempt_id,omitempty"`
	ConfigSHA256         string         `json:"config_sha256,omitempty"`
	Engine               agentv1.Engine `json:"engine,omitempty"`
	FragmentEmitted      bool           `json:"fragment_emitted"`
	ReceiptVerified      bool           `json:"receipt_verified"`
	Reason               Reason         `json:"reason"`
}

type Repository interface {
	RuleNodeDeploymentInput(context.Context, string, string) (Input, error)
}
