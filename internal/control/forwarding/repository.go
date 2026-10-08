package forwarding

import (
	"context"
	"github.com/hongle/hl-panel/internal/control/audit"
)

type CreateInput struct {
	Rule                  Rule
	ProtocolProbeEchoPort int
	RealityPrivateKey     string
	// VLESSSOCKS5Password is confidential runtime material. It must never be
	// copied into Rule or an audit/event projection.
	VLESSSOCKS5Password string
	AutoReality         bool
	IdempotencyKey      string
	RequestSHA256       string
	CreatedBy           string
}

type UpdateInput struct {
	Rule                  Rule
	ProtocolProbeEchoPort int
	RealityPrivateKey     string
	VLESSSOCKS5Password   string
	AutoReality           bool
	ExpectedRevision      int64
	ExpectedCustomerID    string
	IdempotencyKey        string
	RequestSHA256         string
	CreatedBy             string
}

// Repository operations atomically enforce references, group kinds, customer
// authorization and rule quotas, reserve group/protocol ports, persist the
// audit event, and apply idempotency or revision compare-and-swap. Reads derive
// status from current customer state; no activation acknowledgement exists yet.
type Repository interface {
	CreateForwardingRule(context.Context, CreateInput, audit.Event) (Rule, bool, error)
	ListForwardingRules(context.Context) ([]Rule, error)
	ForwardingRule(context.Context, string) (Rule, error)
	UpdateForwardingRule(context.Context, UpdateInput, audit.Event) (Rule, bool, error)
	PreviewForwardingImport(context.Context, []ImportCandidate) ([]ImportEvaluation, error)
	ImportForwardingRules(context.Context, ImportInput, audit.Event) (ImportResult, bool, error)
}

// ActivationWriter persists the result of a complete runtime activation
// check. It is intentionally separate from ordinary rule edits so activation
// can only be recorded after deployment receipts and protocol health agree.
type ActivationWriter interface {
	MarkActivated(context.Context, string, int64, audit.Event) error
}
