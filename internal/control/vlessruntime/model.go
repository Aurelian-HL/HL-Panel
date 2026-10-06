// Package vlessruntime owns the node-scoped confidential material required to
// run a VLESS Reality listener. Public inventory and mutation results never
// contain the customer UUID or the Reality private key.
package vlessruntime

import "time"

type State string

const (
	StateActive  State = "active"
	StateRevoked State = "revoked"
)

func (state State) Valid() bool {
	return state == StateActive || state == StateRevoked
}

// Material is confidential repository state. CredentialUUID and
// RealityPrivateKey are deliberately excluded from JSON so an accidental
// marshal of an internal result cannot turn into an API or audit leak.
type Material struct {
	ID                string     `json:"id"`
	BindingID         string     `json:"binding_id"`
	NodeID            string     `json:"node_id"`
	CustomerID        string     `json:"customer_id"`
	ForwardingRuleID  string     `json:"forwarding_rule_id"`
	EndpointPoolID    string     `json:"endpoint_pool_id"`
	State             State      `json:"state"`
	Revision          int64      `json:"revision"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	RevokedAt         *time.Time `json:"revoked_at,omitempty"`
	CredentialUUID    string     `json:"-"`
	RealityPrivateKey string     `json:"-"`
}

// PublicMaterial is the only representation suitable for ordinary list and
// mutation responses. It intentionally has no secret fields at all.
type PublicMaterial struct {
	ID               string     `json:"id"`
	BindingID        string     `json:"binding_id"`
	NodeID           string     `json:"node_id"`
	CustomerID       string     `json:"customer_id"`
	ForwardingRuleID string     `json:"forwarding_rule_id"`
	EndpointPoolID   string     `json:"endpoint_pool_id"`
	State            State      `json:"state"`
	Revision         int64      `json:"revision"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
}

func (material Material) Public() PublicMaterial {
	var revokedAt *time.Time
	if material.RevokedAt != nil {
		value := *material.RevokedAt
		revokedAt = &value
	}
	return PublicMaterial{
		ID: material.ID, BindingID: material.BindingID, NodeID: material.NodeID,
		CustomerID: material.CustomerID, ForwardingRuleID: material.ForwardingRuleID,
		EndpointPoolID: material.EndpointPoolID, State: material.State,
		Revision: material.Revision, CreatedAt: material.CreatedAt, UpdatedAt: material.UpdatedAt,
		RevokedAt: revokedAt,
	}
}

// SaveInput intentionally accepts only the binding and node references plus
// the node-local private key. The repository resolves the customer UUID from
// the authoritative VLESS identity binding instead of trusting a caller to
// duplicate or replace it.
type SaveInput struct {
	ID                string
	BindingID         string
	NodeID            string
	RealityPrivateKey string
	ExpectedRevision  int64
	IdempotencyKey    string
	RequestSHA256     string
	CreatedBy         string
	CreatedAt         time.Time
}

type RevokeInput struct {
	ID               string
	ExpectedRevision int64
	IdempotencyKey   string
	RequestSHA256    string
	UpdatedBy        string
	UpdatedAt        time.Time
}
