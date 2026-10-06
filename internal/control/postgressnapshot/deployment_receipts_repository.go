package postgressnapshot

import (
	"context"

	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

var _ deploymentreceipts.Repository = (*Store)(nil)

func (s *Store) RuleNodeDeploymentInput(ctx context.Context, ruleID, nodeID string) (deploymentreceipts.Input, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) (deploymentreceipts.Input, error) {
		return state.RuleNodeDeploymentInput(ctx, ruleID, nodeID)
	})
}
