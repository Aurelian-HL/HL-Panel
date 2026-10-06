package endpoints

import (
	"context"

	"github.com/hongle/hl-panel/internal/control/audit"
)

type Repository interface {
	CreateEndpointPool(context.Context, CreatePoolInput, audit.Event) (EndpointPool, bool, error)
	ListEndpointPools(context.Context) ([]EndpointPool, error)
	EndpointPool(context.Context, string) (EndpointPool, error)
	EndpointPoolMembers(context.Context, string) ([]EndpointPoolMember, error)
	AddEndpointPoolMember(context.Context, AddMemberInput, audit.Event) (EndpointPoolMember, bool, error)
	DeleteEndpointPool(context.Context, DeletePoolInput, audit.Event) (bool, error)
}
