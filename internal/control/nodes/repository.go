package nodes

import (
	"context"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
)

type Repository interface {
	NodeByCredentialHash(context.Context, string) (Node, error)
	ListNodes(context.Context) ([]Node, error)
	RotateCredential(context.Context, RotateCredentialInput, audit.Event) (CredentialRotationResult, bool, error)
	UpdateHeartbeat(context.Context, string, Heartbeat, time.Time, audit.Event) (Node, error)
	Overview(context.Context, time.Time, time.Duration) (Overview, error)
	RequestControl(context.Context, ControlCommandInput, audit.Event) (ControlCommandResult, bool, error)
	ControlForNode(context.Context, string) (ControlCommandResult, error)
	RecordControlResult(context.Context, string, ControlCommandResult, audit.Event) error
}
