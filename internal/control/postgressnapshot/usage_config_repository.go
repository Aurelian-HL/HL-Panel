package postgressnapshot

import (
	"context"
	"database/sql"
	"errors"
	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func (s *Store) UsageConfigInput(ctx context.Context, nodeID string, generation int64) (deploymentreceipts.HistoricalInput, error) {
	return readUsage(ctx, s, func(ctx context.Context, tx *sql.Tx, view *memoryrepo.UsageReadView) (deploymentreceipts.HistoricalInput, error) {
		input, err := view.UsageConfigInput(ctx, nodeID, generation)
		if !errors.Is(err, faults.ErrNotFound) {
			return input, err
		}
		// Late usage still resolves archived evidence. Never attach this
		// transaction to the shared cache or promote history to current health.
		var raw []byte
		err = tx.QueryRowContext(ctx, `SELECT payload FROM hl_panel_node_config_history WHERE node_id=$1 AND generation=$2`, nodeID, generation).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			return input, faults.ErrNotFound
		}
		if err != nil {
			return input, errors.New("node configuration history read failed")
		}
		history, err := decodeNodeConfigHistory(raw, nodeID, generation)
		if err != nil {
			return input, err
		}
		return history.UsageInput(), nil
	})
}

func (s *Store) UsageRuleConfigInput(ctx context.Context, nodeID, ruleID string, beforeGeneration int64) (deploymentreceipts.HistoricalInput, error) {
	return readUsage(ctx, s, func(ctx context.Context, tx *sql.Tx, view *memoryrepo.UsageReadView) (deploymentreceipts.HistoricalInput, error) {
		input, err := view.UsageRuleConfigInput(ctx, nodeID, ruleID, beforeGeneration)
		if err != nil && !errors.Is(err, faults.ErrNotFound) {
			return input, err
		}
		// Use the node/generation primary key, return only the latest matching
		// bundle and do not filter out bad attempts. Hot receipts are authoritative
		// even if an older archived copy of the same generation still exists.
		var generation int64
		var raw []byte
		err = tx.QueryRowContext(ctx, `SELECT generation,payload FROM hl_panel_node_config_history
			WHERE node_id=$1 AND generation<$2 AND generation<>ALL($3::bigint[]) AND generation>$6
			AND EXISTS (SELECT 1 FROM jsonb_array_elements(convert_from(payload,'UTF8')::jsonb #> '{Config,config,fragments}') AS fragment
				WHERE fragment->>'group_id'=$4 OR fragment->>'group_id'=$5)
			ORDER BY generation DESC LIMIT 1`, nodeID, beforeGeneration, view.UsageConfigGenerations(nodeID, beforeGeneration),
			generations.ForwardingFragmentID(agentv1.EngineXray, ruleID), generations.ForwardingFragmentID(agentv1.EngineGOST, ruleID), input.Config.Generation).Scan(&generation, &raw)
		if errors.Is(err, sql.ErrNoRows) {
			if input.Config.Generation == 0 {
				return input, faults.ErrNotFound
			}
			return input, nil
		}
		if err != nil {
			return input, errors.New("node rule configuration history read failed")
		}
		history, err := decodeNodeConfigHistory(raw, nodeID, generation)
		if err != nil {
			return input, err
		}
		return history.UsageInput(), nil
	})
}
