package enrollment

import (
	"context"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

type Repository interface {
	CreateEnrollmentToken(context.Context, Token, audit.Event) error
	ListPendingEnrollmentTokens(context.Context, string, time.Time) ([]PendingToken, error)
	ConsumeEnrollmentToken(context.Context, ConsumeInput, time.Time, audit.Event) (nodes.Node, error)
	RevokeEnrollmentToken(context.Context, RevokeInput, audit.Event) (RevokeResult, bool, error)
}
