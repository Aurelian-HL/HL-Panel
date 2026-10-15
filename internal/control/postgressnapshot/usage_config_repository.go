package postgressnapshot

import (
	"context"
	"database/sql"
	"errors"
	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
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
