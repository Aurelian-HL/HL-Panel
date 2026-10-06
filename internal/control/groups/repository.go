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
