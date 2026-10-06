package nodes

import "time"

type Node struct {
	ID                  string            `json:"id"`
	Name                string            `json:"name"`
	Hostname            string            `json:"hostname"`
	NezhaServerID       uint64            `json:"nezha_server_id,omitempty"`
	Platform            string            `json:"platform"`
	Architecture        string            `json:"architecture"`
	AgentVersion        string            `json:"agent_version"`
	Capabilities        []string          `json:"capabilities"`
	BootID              string            `json:"boot_id"`
	EngineVersions      map[string]string `json:"engine_versions"`
	Resources           map[string]any    `json:"resources"`
	DesiredGeneration   int64             `json:"desired_generation"`
	AppliedGeneration   int64             `json:"applied_generation"`
	LastApplyGeneration int64             `json:"last_apply_generation"`
	LastApplyStatus     string            `json:"last_apply_status"`
	LastHeartbeatAt     *time.Time        `json:"last_heartbeat_at"`
	CredentialHash      string            `json:"-"`
	CreatedAt           time.Time         `json:"created_at"`
	UpdatedAt           time.Time         `json:"updated_at"`
}

type View struct {
	Node
	Status string `json:"status"`
}

type Heartbeat struct {
	Hostname                 string
	Platform                 string
	Architecture             string
	BootID                   string
	AgentVersion             string
	EngineVersions           map[string]string
	Resources                map[string]any
	Capabilities             []string
	CurrentAppliedGeneration int64
	LastApplyStatus          string
}

// CredentialRotationResult is safe to persist. NodeCredential is populated
// only on the first response and is deliberately omitted from snapshots and
// audit metadata.
type CredentialRotationResult struct {
	NodeID         string    `json:"node_id"`
	NodeCredential string    `json:"node_credential,omitempty"`
	RotatedAt      time.Time `json:"rotated_at"`
}

type RotateCredentialInput struct {
	NodeID         string
	CredentialHash string
	UpdatedBy      string
	IdempotencyKey string
	RequestSHA256  string
	UpdatedAt      time.Time
}

type Overview struct {
	NodeCount        int `json:"node_count"`
	GroupCount       int `json:"group_count"`
	OnlineNodeCount  int `json:"online_node_count"`
	SyncingNodeCount int `json:"syncing_node_count"`
	FailedApplyCount int `json:"failed_apply_count"`
}
