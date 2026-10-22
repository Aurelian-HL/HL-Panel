package generations

import (
	"context"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

type Repository interface {
	CreateGroupRevision(context.Context, CreateGroupRevisionInput, audit.Event) (CreateGroupRevisionResult, error)
	DesiredNodeConfig(context.Context, string) (NodeConfigGeneration, int64, error)
	RecordApplyResult(context.Context, ApplyResult, audit.Event) (nodes.Node, error)
}

// ApplyResultBatchRepository commits both success receipts atomically. Older
// repositories may continue implementing only Repository.
type ApplyResultBatchRepository interface {
	RecordApplyResults(context.Context, []ApplyResult, []audit.Event) (nodes.Node, error)
}
