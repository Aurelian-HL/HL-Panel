// Package agentv1 defines the version 1 JSON contract shared by the control
// plane and edge agents. It intentionally contains transport types only.
package agentv1

import (
	"encoding/json"
	"time"
)

type NodeConfigGeneration uint64

type GroupRevision uint64

type Engine string

const (
	EngineNodeBundle Engine = "node-bundle"
	EngineXray       Engine = "xray"
	EngineGOST       Engine = "gost"
)

type EnrollmentRequest struct {
	EnrollmentToken string   `json:"token"`
	Hostname        string   `json:"hostname"`
	Platform        string   `json:"platform"`
	Architecture    string   `json:"architecture"`
	AgentVersion    string   `json:"agent_version"`
	Capabilities    []string `json:"capabilities"`
}

type EnrollmentResponse struct {
	NodeID         string `json:"node_id"`
	NodeCredential string `json:"node_credential"`
}

type ResourceSnapshot struct {
	Host              *HostSnapshot `json:"host,omitempty"`
	LogicalCPUs       int           `json:"logical_cpus"`
	GoMaxProcs        int           `json:"go_max_procs"`
	MemoryAllocBytes  uint64        `json:"memory_alloc_bytes"`
	MemorySystemBytes uint64        `json:"memory_system_bytes"`
}

type HeartbeatRequest struct {
	BootID              string               `json:"boot_id"`
	Hostname            string               `json:"hostname"`
	Platform            string               `json:"platform"`
	Architecture        string               `json:"architecture"`
	AgentVersion        string               `json:"agent_version"`
	Capabilities        []string             `json:"capabilities"`
	EngineVersions      map[string]string    `json:"engine_versions"`
	Resources           ResourceSnapshot     `json:"resources"`
	AppliedGeneration   NodeConfigGeneration `json:"applied_generation"`
	AppliedConfigSHA256 string               `json:"applied_config_hash,omitempty"`
	LastApplyPhase      ApplyPhase           `json:"last_apply_phase,omitempty"`
	LastApplyStatus     ApplyStatus          `json:"last_apply_status,omitempty"`
	LastApplyMessage    string               `json:"last_apply_message,omitempty"`
}

type DesiredNodeConfig struct {
	Generation   NodeConfigGeneration `json:"generation"`
	Engine       Engine               `json:"engine"`
	ConfigSHA256 string               `json:"config_sha256"`
	Config       json.RawMessage      `json:"config"`
}

type ConfigurationBundle struct {
	SchemaVersion int                     `json:"schema_version"`
	Fragments     []ConfigurationFragment `json:"fragments"`
}

type ConfigurationFragment struct {
	GroupID       string          `json:"group_id"`
	GroupRevision GroupRevision   `json:"group_generation"`
	Engine        Engine          `json:"engine"`
	Config        json.RawMessage `json:"config"`
}

// EndpointMembership is the compiled ingress scheduling input. It contains a
// single backend address and a short-lived health lease, never client-facing
// subscription credentials or a list of client URIs.
type EndpointMembership struct {
	ID                   string    `json:"id"`
	Address              string    `json:"address"`
	Weight               int       `json:"weight"`
	Status               string    `json:"status"`
	HealthLeaseExpiresAt time.Time `json:"health_lease_expires_at"`
}

type EndpointSelectionPolicy string

const (
	EndpointSelectionWeightedRoundRobin       EndpointSelectionPolicy = "weighted_round_robin"
	EndpointSelectionWeightedLeastConnections EndpointSelectionPolicy = "weighted_least_connections"
	EndpointSelectionRendezvousHash           EndpointSelectionPolicy = "rendezvous_hash"
)

type ApplyPhase string

const (
	ApplyPhasePrepare  ApplyPhase = "prepare"
	ApplyPhaseValidate ApplyPhase = "validate"
	ApplyPhaseCommit   ApplyPhase = "commit"
	ApplyPhaseVerify   ApplyPhase = "verify"
	ApplyPhaseRollback ApplyPhase = "rollback"
)

type ApplyStatus string

const (
	ApplyStatusSucceeded  ApplyStatus = "succeeded"
	ApplyStatusFailed     ApplyStatus = "failed"
	ApplyStatusRolledBack ApplyStatus = "rolled_back"
)

type ApplyResultRequest struct {
	Generation   NodeConfigGeneration `json:"generation"`
	AttemptID    string               `json:"apply_attempt_id,omitempty"`
	Phase        ApplyPhase           `json:"phase"`
	Status       ApplyStatus          `json:"status"`
	ConfigSHA256 string               `json:"config_hash"`
	EngineMode   string               `json:"engine_mode,omitempty"`
	Message      string               `json:"message,omitempty"`
}

const (
	UsageMultiplierScale     int64 = 1_000_000
	UsageMaxMultiplierMicros int64 = 1_000_000_000
)

// UsageReport is an immutable accounting interval. The control plane derives
// charged bytes and authenticates NodeID against the bearer credential.
type UsageReport struct {
	NodeID                string    `json:"node_id"`
	BootID                string    `json:"boot_id"`
	Sequence              int64     `json:"sequence"`
	LegacyRuleID          string    `json:"legacy_rule_id,omitempty"`
	CustomerID            string    `json:"customer_id"`
	RuleID                string    `json:"rule_id"`
	EntryGroupID          string    `json:"entry_group_id"`
	ExitGroupID           string    `json:"exit_group_id"`
	Protocol              string    `json:"protocol"`
	OccurredAt            time.Time `json:"occurred_at"`
	PeriodStartedAt       time.Time `json:"period_started_at"`
	PeriodEndedAt         time.Time `json:"period_ended_at"`
	RuleActualBytes       int64     `json:"rule_actual_bytes"`
	CustomerActualBytes   int64     `json:"customer_actual_bytes"`
	EntryMultiplierMicros int64     `json:"entry_multiplier_micros"`
	ExitMultiplierMicros  int64     `json:"exit_multiplier_micros"`
}

type UsageAcknowledgement struct {
	NodeID   string `json:"node_id"`
	BootID   string `json:"boot_id"`
	Sequence int64  `json:"sequence"`
	Replayed bool   `json:"replayed"`
}

type EnforcementAction string

const (
	EnforcementDisableCustomer EnforcementAction = "disable_customer_access"
	EnforcementEnableCustomer  EnforcementAction = "enable_customer_access"
)

type EnforcementCommand struct {
	DecisionID string            `json:"decision_id"`
	CustomerID string            `json:"customer_id"`
	RuleID     string            `json:"rule_id"`
	Protocol   string            `json:"protocol"`
	Action     EnforcementAction `json:"action"`
	Revision   int64             `json:"revision"`
	Limits     EffectiveLimits   `json:"effective_limits"`
}

// EffectiveLimits is the stricter non-zero value of customer and rule limits.
// All values are zero for UDP because these controls are TCP-only.
type EffectiveLimits struct {
	SpeedLimitMbps  int `json:"speed_limit_mbps"`
	IPLimit         int `json:"ip_limit"`
	ConnectionLimit int `json:"connection_limit"`
}

type EnforcementResultStatus string

const (
	EnforcementResultSucceeded EnforcementResultStatus = "succeeded"
	EnforcementResultFailed    EnforcementResultStatus = "failed"
)

type EnforcementResultRequest struct {
	DecisionID string                  `json:"decision_id"`
	Action     EnforcementAction       `json:"action"`
	Revision   int64                   `json:"revision"`
	Status     EnforcementResultStatus `json:"status"`
	Message    string                  `json:"message,omitempty"`
}
