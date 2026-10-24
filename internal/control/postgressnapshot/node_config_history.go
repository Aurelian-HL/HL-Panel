package postgressnapshot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

// Match the existing snapshot codec limit. Current configurations for several
// large group members can exceed 16 MiB even after history has been detached.
const maxHotSnapshotBytes = memoryrepo.MaxSnapshotBytes

func initializeSnapshotCapacity(ctx context.Context, tx *sql.Tx) error {
	var ready bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_constraint
		WHERE conrelid='nyvp_control_snapshots'::regclass AND conname='hl_panel_snapshot_payload_64m')`).Scan(&ready); err != nil {
		return errors.New("PostgreSQL snapshot capacity lookup failed")
	}
	if ready {
		return nil
	}
	// Initialization already holds the bootstrap advisory lock. Keep the new
	// bound and removal of the old check in the same transaction, before writes.
	if _, err := tx.ExecContext(ctx, `ALTER TABLE nyvp_control_snapshots
		DROP CONSTRAINT IF EXISTS nyvp_control_snapshots_payload_check,
		ADD CONSTRAINT hl_panel_snapshot_payload_64m CHECK (octet_length(payload) BETWEEN 1 AND 67108864)`); err != nil {
		return errors.New("PostgreSQL snapshot capacity migration failed")
	}
	return nil
}

func initializeNodeConfigHistory(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS hl_panel_node_config_history (
		node_id text NOT NULL, generation bigint NOT NULL CHECK(generation > 0),
		payload bytea NOT NULL CHECK(octet_length(payload) BETWEEN 1 AND 16777216),
		PRIMARY KEY(node_id, generation)
	)`); err != nil {
		return errors.New("node configuration history initialization failed")
	}
	state, err := readState(ctx, tx, true)
	if err != nil {
		return err
	}
	history := state.DetachHistoricalNodeConfigs()
	if len(history) == 0 {
		return nil
	}
	if err = persistNodeConfigHistory(ctx, tx, history); err != nil {
		return err
	}
	raw, err := encodeHotSnapshot(state)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET payload=$1,format_version=$2,revision=revision+1,updated_at=clock_timestamp() WHERE singleton=true`, raw, memoryrepo.SnapshotVersion); err != nil {
		return errors.New("node configuration history migration failed")
	}
	return nil
}

func encodeHotSnapshot(state *memoryrepo.Store) ([]byte, error) {
	raw, err := state.EncodeSnapshotWithAudit(nil)
	if err == nil && len(raw) > maxHotSnapshotBytes {
		return nil, errors.New("hot persistence state exceeds snapshot size limit")
	}
	return raw, err
}

func persistNodeConfigHistory(ctx context.Context, tx *sql.Tx, history []memoryrepo.NodeConfigHistory) error {
	for _, h := range history {
		if err := memoryrepo.ValidateNodeConfigHistory(h, h.Config.NodeID, h.Config.Generation); err != nil {
			return err
		}
		raw, err := json.Marshal(h)
		if err != nil {
			return errors.New("node configuration history encoding failed")
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO hl_panel_node_config_history(node_id,generation,payload) VALUES($1,$2,$3)
		 ON CONFLICT(node_id,generation) DO UPDATE SET payload=excluded.payload`, h.Config.NodeID, h.Config.Generation, raw); err != nil {
			return errors.New("node configuration history write failed")
		}
	}
	return nil
}

func decodeNodeConfigHistory(raw []byte, nodeID string, generation int64) (memoryrepo.NodeConfigHistory, error) {
	var h memoryrepo.NodeConfigHistory
	if json.Unmarshal(raw, &h) != nil || memoryrepo.ValidateNodeConfigHistory(h, nodeID, generation) != nil {
		return h, errors.New("node configuration history invalid")
	}
	return h, nil
}

func attachNodeConfigHistoryReader(ctx context.Context, tx *sql.Tx, state *memoryrepo.Store) {
	state.SetNodeConfigHistoryReader(func(_ context.Context, nodeID string, generation int64) (memoryrepo.NodeConfigHistory, error) {
		var raw []byte
		err := tx.QueryRowContext(ctx, `SELECT payload FROM hl_panel_node_config_history WHERE node_id=$1 AND generation=$2`, nodeID, generation).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			return memoryrepo.NodeConfigHistory{}, faults.ErrNotFound
		}
		if err != nil {
			return memoryrepo.NodeConfigHistory{}, errors.New("node configuration history read failed")
		}
		return decodeNodeConfigHistory(raw, nodeID, generation)
	})
}

func joinNodeConfigHistory(ctx context.Context, tx *sql.Tx, state *memoryrepo.Store) error {
	rows, err := tx.QueryContext(ctx, `SELECT node_id,generation,payload FROM hl_panel_node_config_history ORDER BY node_id,generation`)
	if err != nil {
		return errors.New("node configuration history export failed")
	}
	defer rows.Close()
	total := 0
	for rows.Next() {
		var nodeID string
		var generation int64
		var raw []byte
		if err = rows.Scan(&nodeID, &generation, &raw); err != nil {
			return errors.New("node configuration history export read failed")
		}
		total += len(raw)
		if total > memoryrepo.MaxSnapshotBytes {
			return errors.New("node configuration history exceeds migration archive limit")
		}
		h, err := decodeNodeConfigHistory(raw, nodeID, generation)
		if err != nil {
			return err
		}
		if err = state.RestoreNodeConfigHistory([]memoryrepo.NodeConfigHistory{h}); err != nil {
			return err
		}
	}
	return rows.Err()
}
