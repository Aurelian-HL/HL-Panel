package groupconfig

import (
	"context"
	"github.com/hongle/hl-panel/internal/control/audit"
)

type UpdateInput struct {
	Network          GroupNetwork
	ExpectedRevision int64
	IdempotencyKey   string
	RequestSHA256    string
	CreatedBy        string
}

// Repository validates device-group references and kinds, and rejects changes
// that invalidate existing rules. Port ranges are mandatory for entry groups;
// EXIT groups may keep a zero range and an empty host. All writes and their
// audit/idempotency records commit in one transaction.
type Repository interface {
	ListGroupNetworks(context.Context) ([]GroupNetwork, error)
	GroupNetwork(context.Context, string) (GroupNetwork, error)
	UpdateGroupNetwork(context.Context, UpdateInput, audit.Event) (GroupNetwork, bool, error)
}
