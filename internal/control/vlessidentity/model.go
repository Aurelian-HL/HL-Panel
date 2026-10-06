// Package vlessidentity owns the lifecycle of customer VLESS credentials.
// A binding points at one stable endpoint pool; device-group members remain an
// internal scheduling concern and are never part of this public model.
package vlessidentity

import "time"

type State string

const (
	StateActive  State = "active"
	StateRevoked State = "revoked"
)

func (state State) Valid() bool {
	return state == StateActive || state == StateRevoked
}

// Binding is safe for administrator list and mutation responses. The VLESS
// UUID deliberately lives only in CredentialRecord and is not JSON serializable.
type Binding struct {
	ID               string     `json:"id"`
	CustomerID       string     `json:"customer_id"`
	ForwardingRuleID string     `json:"forwarding_rule_id"`
	EndpointPoolID   string     `json:"endpoint_pool_id"`
	State            State      `json:"state"`
	Revision         int64      `json:"revision"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
}

// CredentialRecord is confidential repository state. CredentialUUID must not
// be returned by list APIs, audit metadata, errors, or ordinary JSON.
type CredentialRecord struct {
	Binding        Binding `json:"binding"`
	CredentialUUID string  `json:"-"`
}

type ProvisionRequest struct {
	CustomerID       string `json:"customer_id"`
	ForwardingRuleID string `json:"forwarding_rule_id"`
	EndpointPoolID   string `json:"endpoint_pool_id"`
	Revision         int64  `json:"revision"`
}

type MutationRequest struct {
	Revision int64 `json:"revision"`
}

type ProvisionInput struct {
	Record           CredentialRecord
	ExpectedRevision int64
	IdempotencyKey   string
	RequestSHA256    string
	CreatedBy        string
}

type RotateInput struct {
	BindingID        string
	CredentialUUID   string
	ExpectedRevision int64
	IdempotencyKey   string
	RequestSHA256    string
	UpdatedBy        string
	UpdatedAt        time.Time
}

type RevokeInput struct {
	BindingID        string
	ExpectedRevision int64
	IdempotencyKey   string
	RequestSHA256    string
	UpdatedBy        string
	UpdatedAt        time.Time
}
