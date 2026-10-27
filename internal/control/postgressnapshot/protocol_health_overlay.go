package postgressnapshot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

func initializeProtocolHealthOverlays(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS hl_panel_protocol_health (
        rule_id text NOT NULL, node_id text NOT NULL, payload bytea NOT NULL,
        PRIMARY KEY(rule_id,node_id), CHECK(octet_length(payload) BETWEEN 1 AND 65536)
    )`)
	if err != nil {
		return errors.New("protocol health persistence initialization failed")
	}
	return nil
}

func mergeProtocolHealthOverlays(ctx context.Context, tx *sql.Tx, state *memoryrepo.Store) error {
	observations, err := readProtocolHealthOverlays(ctx, tx)
	if err != nil {
		return err
	}
	state.MergeProtocolHealthOverlays(observations)
	return nil
}

func readProtocolHealthOverlays(ctx context.Context, tx *sql.Tx) ([]gatewaymembership.ProtocolObservation, error) {
	rows, err := tx.QueryContext(ctx, `SELECT payload FROM hl_panel_protocol_health ORDER BY rule_id,node_id`)
	if err != nil {
		return nil, errors.New("protocol health projection read failed")
	}
	defer rows.Close()
	var observations []gatewaymembership.ProtocolObservation
	for rows.Next() {
		var raw []byte
		var observation gatewaymembership.ProtocolObservation
		if err = rows.Scan(&raw); err != nil {
			return nil, errors.New("protocol health projection read failed")
		}
		if err = json.Unmarshal(raw, &observation); err != nil {
			return nil, errors.New("protocol health projection decode failed")
		}
		observations = append(observations, observation)
	}
	if rows.Err() != nil {
		return nil, errors.New("protocol health projection iteration failed")
	}
	return observations, nil
}

// Renewal is serialized by the same row lock as business edits, but writes
// only the accepted small proofs and the revision metadata. First activation
// still uses the normal audited snapshot transaction.
func (s *Store) publishProtocolHealth(parent context.Context, observations []gatewaymembership.ProtocolObservation, activate bool) ([]error, error) {
	ctx, cancel := context.WithTimeout(parent, operationTimeout)
	defer cancel()
	release, err := s.writeGate.acquire(ctx)
	if err != nil {
		return nil, snapshotFailure(ctx, "write_queue", err)
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, snapshotFailure(ctx, "begin", err)
	}
	defer tx.Rollback()
	// Build a cold immutable view before locking the business row. Once the
	// lock is acquired, revalidate its identity before checking any proof.
	cached, _, err := s.validatedReadSnapshot(ctx, tx)
	if err != nil {
		return nil, err
	}
	cached, err = s.lockReadSnapshot(ctx, tx, cached)
	if err != nil {
		return nil, err
	}
	traffic, health, err := s.forwardingOverlays(ctx, tx)
	if err != nil {
		return nil, snapshotFailure(ctx, "overlay_read", err)
	}
	view := cached.forwarding.WithOverlays(uint64(cached.revision), traffic, health, nil)
	results, changed, needsActivation, err := view.CheckProtocolHealthRenewal(ctx, observations)
	if err != nil {
		return nil, snapshotOperationFailure(ctx, err)
	}
	apply := func(m *memoryrepo.Store) ([]error, error) {
		if activate {
			return m.PublishProtocolHealthBatch(parent, observations)
		}
		results := make([]error, len(observations))
		for i, observation := range observations {
			results[i] = m.PublishProtocolHealth(parent, observation)
		}
		return results, nil
	}
	if activate && needsActivation && changed {
		if err = tx.Rollback(); err != nil {
			return nil, snapshotFailure(ctx, "read_rollback", err)
		}
		// The audited activation takes the same gate. Release the preliminary
		// renewal attempt after rollback before entering that transaction.
		release()
		release = nil
		// Use parent context on replay so a small pool releases its connection
		// before waiting for the normal audited write.
		return transact(parent, s, true, apply)
	}
	if changed {
		// Renewal only changes overlays and metadata. The prepared restore still
		// represents these exact payload bytes; merge overlays after locking on
		// its next use. A different MVCC row must never inherit this shortcut.
		preparedMatches := s.preparedWrite.state != nil && s.preparedWrite.version == cached.version &&
			s.preparedWrite.revision == cached.revision && s.preparedWrite.transactionID == cached.transactionID
		if err = writeProtocolHealthOverlays(ctx, tx, observations, results); err != nil {
			return nil, snapshotFailure(ctx, "overlay_write", err)
		}
		if err = tx.QueryRowContext(ctx, `UPDATE nyvp_control_snapshots SET revision=revision+1,updated_at=clock_timestamp() WHERE singleton=true RETURNING revision,xmin::text`).Scan(&cached.revision, &cached.transactionID); err != nil {
			return nil, snapshotFailure(ctx, "snapshot_update", err)
		}
		if !preparedMatches {
			s.preparedWrite = preparedSnapshotWrite{}
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, snapshotFailure(ctx, "commit", err)
	}
	if changed {
		if s.preparedWrite.state != nil {
			s.preparedWrite.revision, s.preparedWrite.transactionID = cached.revision, cached.transactionID
			s.preparedWrite.state.SetPersistenceRevision(uint64(cached.revision))
		}
		// The immutable base payload/identity/usage views did not change.
		s.readMu.Lock()
		if s.readCache.revision <= cached.revision {
			s.readCache = cached
		}
		s.readMu.Unlock()
	}
	return results, nil
}
