// Package usage owns immutable traffic accounting and quota-enforcement
// projections. It intentionally does not activate or disable data-plane rules.
package usage

import (
	"context"
	"time"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

const (
	MultiplierScale     int64 = agentv1.UsageMultiplierScale
	MaxMultiplierMicros int64 = agentv1.UsageMaxMultiplierMicros
)

type Scope string

const (
	ScopeSite        Scope = "site"
	ScopeCustomer    Scope = "customer"
	ScopeRule        Scope = "rule"
	ScopeDeviceGroup Scope = "device_group"
)

func (scope Scope) Valid() bool {
	switch scope {
	case ScopeSite, ScopeCustomer, ScopeRule, ScopeDeviceGroup:
		return true
	default:
		return false
	}
}

// Report is the node-authenticated accounting interval submitted by an edge
// agent. ChargedBytes is never accepted from the node; the control plane
// derives it from CustomerActualBytes and the two multiplier snapshots.
type Report struct {
	ConfigGeneration      agentv1.NodeConfigGeneration `json:"config_generation,omitempty"`
	NodeID                string                       `json:"node_id"`
	BootID                string                       `json:"boot_id"`
	Sequence              int64                        `json:"sequence"`
	LegacyRuleID          string                       `json:"legacy_rule_id,omitempty"`
	CustomerID            string                       `json:"customer_id"`
	RuleID                string                       `json:"rule_id"`
	EntryGroupID          string                       `json:"entry_group_id"`
	ExitGroupID           string                       `json:"exit_group_id"`
	Protocol              string                       `json:"protocol"`
	OccurredAt            time.Time                    `json:"occurred_at"`
	PeriodStartedAt       time.Time                    `json:"period_started_at"`
	PeriodEndedAt         time.Time                    `json:"period_ended_at"`
	RuleActualBytes       int64                        `json:"rule_actual_bytes"`
	CustomerActualBytes   int64                        `json:"customer_actual_bytes"`
	EntryMultiplierMicros int64                        `json:"entry_multiplier_micros"`
	ExitMultiplierMicros  int64                        `json:"exit_multiplier_micros"`
}

type Event struct {
	Report
	ChargedBytes  int64     `json:"charged_bytes"`
	PayloadSHA256 string    `json:"payload_sha256"`
	ReceivedAt    time.Time `json:"received_at"`
}

type CustomerTotals struct {
	CustomerID          string    `json:"customer_id"`
	ActualBytes         int64     `json:"actual_bytes"`
	ChargedBytes        int64     `json:"charged_bytes"`
	LastUsageOccurredAt time.Time `json:"last_usage_occurred_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type CustomerPolicy struct {
	CustomerID        string
	Disabled          bool
	ExpiresAt         *time.Time
	TrafficLimitBytes int64
}

// LegacyRuleMetadata is resolved by the control plane from the rule assigned
// to the authenticated node. Agent-supplied customer and group IDs are never
// used to fill an old stats tag.
type LegacyRuleMetadata struct {
	EntryMultiplierMicros int64
	ExitMultiplierMicros  int64
	RuleID                string
	CustomerID            string
	EntryGroup            string
	ExitGroup             string
	Protocol              string
}

type GenerationRuleMetadataProvider interface {
	ResolveRuleUsageMetadataAt(context.Context, string, string, agentv1.NodeConfigGeneration, time.Time) (LegacyRuleMetadata, error)
}

type LegacyRuleMetadataProvider interface {
	ResolveLegacyRuleUsageMetadata(context.Context, string, string) (LegacyRuleMetadata, error)
}

type EnforcementReason string

const (
	EnforcementCustomerDisabled EnforcementReason = "customer_disabled"
	EnforcementCustomerExpired  EnforcementReason = "customer_expired"
	EnforcementQuotaExhausted   EnforcementReason = "quota_exhausted"
)

type EnforcementStatus string

const (
	EnforcementPending       EnforcementStatus = "pending"
	EnforcementApplied       EnforcementStatus = "applied"
	EnforcementApplyFailed   EnforcementStatus = "apply_failed"
	EnforcementRevokePending EnforcementStatus = "revoke_pending"
	EnforcementRevoked       EnforcementStatus = "revoked"
	EnforcementRevokeFailed  EnforcementStatus = "revoke_failed"
)

// EnforcementDecision is advisory until a future authenticated executor
// applies it and records a separate result. This module never claims success.
type EnforcementDecision struct {
	ID                   string                    `json:"id"`
	CustomerID           string                    `json:"customer_id"`
	RuleID               string                    `json:"rule_id"`
	Protocol             string                    `json:"protocol"`
	Reason               EnforcementReason         `json:"reason"`
	Action               agentv1.EnforcementAction `json:"action"`
	Status               EnforcementStatus         `json:"status"`
	TriggerNodeID        string                    `json:"trigger_node_id"`
	TriggerBootID        string                    `json:"trigger_boot_id"`
	TriggerSequence      int64                     `json:"trigger_sequence"`
	CustomerChargedBytes int64                     `json:"customer_charged_bytes"`
	TrafficLimitBytes    int64                     `json:"traffic_limit_bytes"`
	CreatedAt            time.Time                 `json:"created_at"`
	UpdatedAt            time.Time                 `json:"updated_at"`
	Revision             int64                     `json:"revision"`
	LastError            string                    `json:"last_error"`
}

type EnforcementResult struct {
	DecisionID    string                          `json:"decision_id"`
	NodeID        string                          `json:"node_id"`
	Action        agentv1.EnforcementAction       `json:"action"`
	Revision      int64                           `json:"revision"`
	Status        agentv1.EnforcementResultStatus `json:"status"`
	Message       string                          `json:"message"`
	PayloadSHA256 string                          `json:"payload_sha256"`
	CreatedAt     time.Time                       `json:"created_at"`
}

type RevokeRequest struct {
	DecisionID      string
	AdministratorID string
	IdempotencyKey  string
	RequestSHA256   string
	RequestedAt     time.Time
}

type LimitPolicyProvider interface {
	EffectiveLimits(context.Context, string, string) (agentv1.EffectiveLimits, string, error)
}

type IngestResult struct {
	Event          Event                `json:"event"`
	CustomerTotals CustomerTotals       `json:"customer_totals"`
	Decision       *EnforcementDecision `json:"enforcement_decision,omitempty"`
	Replayed       bool                 `json:"replayed"`
}

type Query struct {
	Scope    Scope
	ScopeID  string
	From     *time.Time
	To       *time.Time
	Page     int
	PageSize int
}

type Record struct {
	Event
	Decision *EnforcementDecision `json:"enforcement_decision,omitempty"`
}

type Totals struct {
	RuleActualBytes     int64 `json:"rule_actual_bytes"`
	CustomerActualBytes int64 `json:"customer_actual_bytes"`
	ChargedBytes        int64 `json:"charged_bytes"`
}

type QueryResult struct {
	Items    []Record `json:"items"`
	Totals   Totals   `json:"totals"`
	Total    int64    `json:"total"`
	Page     int      `json:"page"`
	PageSize int      `json:"page_size"`
}
