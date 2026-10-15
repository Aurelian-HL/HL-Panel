package groups

import (
	"context"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/generations"
)

type Repository interface {
	CreateDeviceGroup(context.Context, DeviceGroup, audit.Event) error
	UpdateDeviceGroup(context.Context, UpdateInput, audit.Event) (DeviceGroup, bool, error)
	DeleteDeviceGroup(context.Context, DeleteInput, audit.Event) (bool, error)
	ListDeviceGroups(context.Context) ([]DeviceGroup, error)
	ListGroupMembers(context.Context, string) ([]Member, error)
	UpsertGroupMember(context.Context, Member, audit.Event) (Member, []generations.NodeConfigGeneration, error)
	UpdateGroupMemberWeight(context.Context, UpdateMemberWeightInput, audit.Event) (UpdateMemberWeightResult, error)
	RetireGroupMember(context.Context, string, string, time.Time, audit.Event) (RetireMemberResult, error)
}

// The optional idempotent mutation interfaces keep the legacy repository
// contract usable by small test adapters while allowing production stores to
// persist replay records together with the group snapshot.
type CreateDeviceGroupInput struct {
	Group          DeviceGroup
	CreatedBy      string
	IdempotencyKey string
	RequestSHA256  string
}

type UpsertGroupMemberInput struct {
	Member         Member
	CreatedBy      string
	IdempotencyKey string
	RequestSHA256  string
}

type RetireGroupMemberInput struct {
	GroupID        string
	NodeID         string
	RetiredAt      time.Time
	RetiredBy      string
	IdempotencyKey string
	RequestSHA256  string
}

type IdempotentRepository interface {
	CreateDeviceGroupIdempotent(context.Context, CreateDeviceGroupInput, audit.Event) (DeviceGroup, bool, error)
	UpsertGroupMemberIdempotent(context.Context, UpsertGroupMemberInput, audit.Event) (Member, []generations.NodeConfigGeneration, bool, error)
	RetireGroupMemberIdempotent(context.Context, RetireGroupMemberInput, audit.Event) (RetireMemberResult, error)
}
