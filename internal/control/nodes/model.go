package nodes

import "time"

type Node struct {
	ID                      string            `json:"id"`
	Name                    string            `json:"name"`
	Hostname                string            `json:"hostname"`
	DialHost                string            `json:"dial_host,omitempty"`
	NezhaServerID           uint64            `json:"nezha_server_id,omitempty"`
	Platform                string            `json:"platform"`
	Architecture            string            `json:"architecture"`
	AgentVersion            string            `json:"agent_version"`
	Capabilities            []string          `json:"capabilities"`
	BootID                  string            `json:"boot_id"`
	EngineVersions          map[string]string `json:"engine_versions"`
	Resources               map[string]any    `json:"resources"`
	DesiredGeneration       int64             `json:"desired_generation"`
	AppliedGeneration       int64             `json:"applied_generation"`
	LastApplyGeneration     int64             `json:"last_apply_generation"`
	LastApplyStatus         string            `json:"last_apply_status"`
	LastHeartbeatAt         *time.Time        `json:"last_heartbeat_at"`
	DeletedAt               *time.Time        `json:"deleted_at,omitempty"`
	CredentialHash          string            `json:"-"`
	CreatedAt               time.Time         `json:"created_at"`
	UpdatedAt               time.Time         `json:"updated_at"`
	ControlCommandID        string            `json:"control_command_id,omitempty"`
	ControlCommand          string            `json:"control_command,omitempty"`
	ControlCommandStatus    string            `json:"control_command_status,omitempty"`
	ControlCommandMessage   string            `json:"control_command_message,omitempty"`
	ControlCommandLogs      string            `json:"control_command_logs,omitempty"`
	ControlCommandUpdatedAt *time.Time        `json:"control_command_updated_at,omitempty"`
	ControlIdempotencyKey   string            `json:"-"`
	ControlRequestSHA256    string            `json:"-"`
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
	ControlCommandStatus     string
	ControlCommandMessage    string
	ControlCommandLogs       string
}

type ControlCommandInput struct {
	NodeID          string
	Command         string
	AdministratorID string
	IdempotencyKey  string
	RequestSHA256   string
	CommandID       string
	UpdatedAt       time.Time
}

type ControlCommandResult struct {
	NodeID    string    `json:"node_id"`
	CommandID string    `json:"command_id"`
	Command   string    `json:"command"`
	Status    string    `json:"status"`
	Message   string    `json:"message"`
	Logs      string    `json:"logs"`
	UpdatedAt time.Time `json:"updated_at"`
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
	NodeCount        int    `json:"node_count"`
	GroupCount       int    `json:"group_count"`
	OnlineNodeCount  int    `json:"online_node_count"`
	SyncingNodeCount int    `json:"syncing_node_count"`
	FailedApplyCount int    `json:"failed_apply_count"`
	Nodes            []View `json:"nodes"`
}
