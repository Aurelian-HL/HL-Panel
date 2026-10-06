package usage

import "context"

// DecisionProjector is invoked only for a newly persisted event, after the
// repository has atomically calculated the post-ingest customer totals. It
// keeps business policy in Service while allowing event, total, cursor and
// advisory decision to commit as one transaction.
type DecisionProjector func(CustomerTotals) (*EnforcementDecision, error)

type Repository interface {
	Ingest(context.Context, Event, DecisionProjector) (IngestResult, error)
	Query(context.Context, Query) (QueryResult, error)
	DesiredEnforcement(context.Context, string) (*EnforcementDecision, error)
	RecordEnforcementResult(context.Context, EnforcementResult) (EnforcementDecision, bool, error)
	RequestRevoke(context.Context, RevokeRequest) (EnforcementDecision, bool, error)
}

// LegacyReplayRepository recognizes an exact retry of an already-ingested
// rule report (legacy or full format) before current metadata is resolved. This keeps a lost
// acknowledgement from blocking the agent journal after rule state changes.
type LegacyReplayRepository interface {
	ReplayLegacy(context.Context, Report) (IngestResult, bool, error)
}

type CustomerPolicyProvider interface {
	UsagePolicy(context.Context, string) (CustomerPolicy, error)
}
