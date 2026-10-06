package generations

import (
	"encoding/json"
	"time"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type GroupRevision struct {
	ID             string          `json:"id"`
	GroupID        string          `json:"group_id"`
	Revision       int64           `json:"generation"`
	Engine         agentv1.Engine  `json:"engine"`
	Config         json.RawMessage `json:"config,omitempty"`
	ConfigSHA256   string          `json:"config_hash"`
	RequestSHA256  string          `json:"-"`
	IdempotencyKey string          `json:"-"`
	CreatedBy      string          `json:"created_by"`
	CreatedAt      time.Time       `json:"created_at"`
}

type NodeConfigGeneration struct {
	ID           string          `json:"id"`
	NodeID       string          `json:"node_id"`
	Generation   int64           `json:"generation"`
	Engine       agentv1.Engine  `json:"engine"`
	Config       json.RawMessage `json:"config"`
	ConfigSHA256 string          `json:"config_hash"`
	CreatedAt    time.Time       `json:"created_at"`
}

type CreateGroupRevisionInput struct {
	ID             string
	GroupID        string
	Engine         agentv1.Engine
	Config         json.RawMessage
	ConfigSHA256   string
	RequestSHA256  string
	IdempotencyKey string
	CreatedBy      string
	CreatedAt      time.Time
}

type CreateGroupRevisionResult struct {
	Generation  GroupRevision          `json:"generation"`
	Assignments []NodeConfigGeneration `json:"assignments"`
	Replayed    bool                   `json:"replayed"`
}

type ApplyResult struct {
	ID           string              `json:"id"`
	NodeID       string              `json:"node_id"`
	Generation   int64               `json:"generation"`
	AttemptID    string              `json:"apply_attempt_id,omitempty"`
	Phase        agentv1.ApplyPhase  `json:"phase"`
	Status       agentv1.ApplyStatus `json:"status"`
	ConfigSHA256 string              `json:"config_hash"`
	// EngineMode is empty for legacy and dry-run acknowledgements. Only an
	// explicitly reported real engine mode can support a deployment receipt.
	EngineMode string    `json:"engine_mode,omitempty"`
	Message    string    `json:"message"`
	CreatedAt  time.Time `json:"created_at"`
}

type ApplyAttemptState struct {
	CurrentID   string          `json:"current_id"`
	SeenIDs     map[string]bool `json:"seen_ids"`
	Invalidated bool            `json:"invalidated,omitempty"`
}
