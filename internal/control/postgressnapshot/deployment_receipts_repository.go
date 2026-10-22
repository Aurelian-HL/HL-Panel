package postgressnapshot

import (
	"context"

	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

var _ deploymentreceipts.Repository = (*Store)(nil)

func (s *Store) RuleNodeDeploymentInput(ctx context.Context, ruleID, nodeID string) (deploymentreceipts.Input, error) {
	return readForwarding(ctx, s, func(state *memoryrepo.ForwardingReadView) (deploymentreceipts.Input, error) {
		return state.RuleNodeDeploymentInput(ctx, ruleID, nodeID)
	})
}
