package enrollment

import (
	"time"

	"github.com/hongle/hl-panel/internal/control/nodes"
)

type Token struct {
	ID   string
	Name string
	// GroupID scopes NY-style group-first enrollment tokens. Empty retains
	// the legacy global registration flow.
	GroupID       string
	NezhaServerID uint64
	TokenHash     string
	ExpiresAt     time.Time
	UsedAt        *time.Time
	UsedNodeID    string `json:",omitempty"`
	RevokedAt     *time.Time
	CreatedBy     string
	CreatedAt     time.Time
}

type IssueResult struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Token         string    `json:"token"`
	ExpiresAt     time.Time `json:"expires_at"`
	NezhaServerID uint64    `json:"nezha_server_id,omitempty"`
}

// PendingToken is safe to return to administrators; it contains no bearer
// material or token hash.
type PendingToken struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	GroupID       string    `json:"group_id"`
	ExpiresAt     time.Time `json:"expires_at"`
	CreatedAt     time.Time `json:"created_at"`
	NezhaServerID uint64    `json:"nezha_server_id,omitempty"`
}

type EnrollInput struct {
	RawToken         string
	EnrollmentSecret string
	Hostname         string
	DialHost         string
	Platform         string
	Architecture     string
	AgentVersion     string
	Capabilities     []string
}

type EnrollResult struct {
	NodeID         string `json:"node_id"`
	NodeCredential string `json:"node_credential"`
}

type ConsumeInput struct {
	TokenHash      string
	Node           nodes.Node
	CredentialHash string
	AllowReplay    bool
	Reenroll       bool
}

type RevokeInput struct {
	ID             string
	RevokedBy      string
	IdempotencyKey string
	RequestSHA256  string
	RevokedAt      time.Time
}

type RevokeResult struct {
	ID        string    `json:"id"`
	RevokedAt time.Time `json:"revoked_at"`
}
