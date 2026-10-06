package endpoints

import (
	"time"

	"github.com/hongle/hl-panel/internal/routing/scheduler"
)

// Mode describes what the customer receives. The first production mode is a
// single stable service endpoint; the member list is an internal server-side
// candidate pool and is never serialized into a customer subscription.
type Mode string

const ModeSingleServiceEndpoint Mode = "SINGLE_SERVICE_ENDPOINT"

func (m Mode) Valid() bool { return m == ModeSingleServiceEndpoint }

type SelectionPolicy = scheduler.SelectionPolicy

const (
	SelectionWeightedRoundRobin       = scheduler.PolicyWeightedRoundRobin
	SelectionWeightedLeastConnections = scheduler.PolicyWeightedLeastConnections
	SelectionRendezvousHash           = scheduler.PolicyRendezvousHash
)

func ValidSelectionPolicy(policy SelectionPolicy) bool {
	switch policy {
	case SelectionWeightedRoundRobin, SelectionWeightedLeastConnections, SelectionRendezvousHash:
		return true
	default:
		return false
	}
}

type CandidateState string

const (
	CandidateEligible    CandidateState = "eligible"
	CandidateDraining    CandidateState = "draining"
	CandidateQuarantined CandidateState = "quarantined"
	CandidateDisabled    CandidateState = "disabled"
)

func (s CandidateState) Valid() bool {
	switch s {
	case CandidateEligible, CandidateDraining, CandidateQuarantined, CandidateDisabled:
		return true
	default:
		return false
	}
}

type EndpointPool struct {
	ID                    string          `json:"id"`
	OwnerID               string          `json:"owner_id,omitempty"`
	Name                  string          `json:"name"`
	GroupID               string          `json:"group_id"`
	RuleID                string          `json:"rule_id,omitempty"`
	Mode                  Mode            `json:"mode"`
	Protocol              string          `json:"protocol"`
	Hostname              string          `json:"hostname"`
	Port                  int             `json:"port"`
	SelectionPolicy       SelectionPolicy `json:"selection_policy"`
	CreatedAt             time.Time       `json:"created_at"`
	UpdatedAt             time.Time       `json:"updated_at"`
	MemberCount           int             `json:"member_count"`
	HealthyCandidateCount int             `json:"healthy_candidate_count"`
}

type EndpointPoolMember struct {
	PoolID            string         `json:"pool_id"`
	GroupID           string         `json:"group_id"`
	NodeID            string         `json:"node_id"`
	DialHost          string         `json:"dial_host,omitempty"`
	Weight            int            `json:"weight"`
	Priority          int            `json:"priority"`
	State             CandidateState `json:"state"`
	LastHealthAt      *time.Time     `json:"last_health_at,omitempty"`
	LastHealthReason  string         `json:"last_health_reason,omitempty"`
	ActiveConnections int64          `json:"active_connections"`
	UpdatedAt         time.Time      `json:"updated_at"`
}

// CandidateEligibleForNewConnection is deliberately strict: a member must be
// explicitly eligible and have a recent successful health observation. The
// scheduler, not DNS, applies weight/least-connection/hash selection.
func (m EndpointPoolMember) CandidateEligibleForNewConnection(now time.Time, healthTTL time.Duration) bool {
	if m.State != CandidateEligible || m.Weight < 1 || m.ActiveConnections < 0 || m.LastHealthAt == nil {
		return false
	}
	return healthTTL > 0 && !m.LastHealthAt.After(now) && now.Sub(*m.LastHealthAt) <= healthTTL
}

type CreatePoolInput struct {
	ID              string
	Name            string
	GroupID         string
	RuleID          string
	Mode            Mode
	Protocol        string
	Hostname        string
	Port            int
	SelectionPolicy SelectionPolicy
	IdempotencyKey  string
	RequestSHA256   string
	CreatedBy       string
	CreatedAt       time.Time
}

type AddMemberInput struct {
	PoolID         string
	GroupID        string
	NodeID         string
	Weight         int
	Priority       int
	IdempotencyKey string
	RequestSHA256  string
	CreatedBy      string
	CreatedAt      time.Time
}

type DeletePoolInput struct {
	PoolID         string
	IdempotencyKey string
	RequestSHA256  string
	DeletedBy      string
	DeletedAt      time.Time
}
