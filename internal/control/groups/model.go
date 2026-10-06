package groups

import (
	"time"

	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/generations"
)

type Kind string

type SelectionPolicy = endpoints.SelectionPolicy

const (
	KindEntry  Kind = "ENTRY"
	KindExit   Kind = "EXIT"
	KindEdge   Kind = "EDGE"
	KindHybrid Kind = "HYBRID"
)

func (k Kind) Valid() bool {
	switch k {
	case KindEntry, KindExit, KindEdge, KindHybrid:
		return true
	default:
		return false
	}
}

func (k Kind) Creatable() bool {
	return k == KindEntry || k == KindExit
}

type DeviceGroup struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Kind             Kind            `json:"kind"`
	UserGroupID      string          `json:"user_group_id"`
	HideInProbe      bool            `json:"hide_in_probe"`
	SelectionPolicy  SelectionPolicy `json:"selection_policy"`
	Description      string          `json:"description"`
	MemberCount      int             `json:"member_count"`
	CurrentRevision  int64           `json:"current_generation"`
	MetadataRevision int64           `json:"metadata_revision"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

// UpdateInput is the complete editable portion of a device group. The kind
// and identifier stay server-owned so an edit cannot silently change routing kind.
type UpdateInput struct {
	Group            DeviceGroup
	ExpectedRevision int64
	IdempotencyKey   string
	RequestSHA256    string
	CreatedBy        string
}

// DeleteInput carries the authenticated mutation identity and its idempotency
// fingerprint. Deletion is deliberately refuse-by-default when any routing
// object still references the group.
type DeleteInput struct {
	GroupID        string
	IdempotencyKey string
	RequestSHA256  string
	DeletedBy      string
	DeletedAt      time.Time
}

type Member struct {
	GroupID   string     `json:"group_id"`
	NodeID    string     `json:"node_id"`
	DialHost  string     `json:"dial_host,omitempty"`
	Weight    int        `json:"weight"`
	Priority  int        `json:"priority"`
	RetiredAt *time.Time `json:"retired_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type UpdateMemberWeightInput struct {
	GroupID, NodeID                          string
	Weight                                   int
	ExpectedUpdatedAt                        time.Time
	UpdatedAt                                time.Time
	IdempotencyKey, RequestSHA256, UpdatedBy string
}

type UpdateMemberWeightResult struct {
	Member      Member                             `json:"member"`
	Assignments []generations.NodeConfigGeneration `json:"assignments"`
	Replayed    bool                               `json:"replayed"`
}

type RetireMemberResult struct {
	Member      Member                             `json:"member"`
	Assignments []generations.NodeConfigGeneration `json:"assignments"`
	Replayed    bool                               `json:"replayed"`
}
