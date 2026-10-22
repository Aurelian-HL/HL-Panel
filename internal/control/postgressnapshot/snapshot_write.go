package postgressnapshot

import (
	"context"
	"database/sql"
	"errors"

	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

type preparedSnapshotWrite struct {
	version       int
	revision      int64
	transactionID string
	state         *memoryrepo.Store
}

// Preparing is read-only: it neither locks the business row nor executes a
// caller's mutation. A later locking read validates the exact MVCC row, including
// external repairs that replace a payload without advancing its revision.
func prepareSnapshotWrite(ctx context.Context, tx *sql.Tx) (preparedSnapshotWrite, error) {
	var prepared preparedSnapshotWrite
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT format_version, revision, xmin::text, payload
 FROM nyvp_control_snapshots WHERE singleton=true`).Scan(&prepared.version, &prepared.revision, &prepared.transactionID, &raw); err != nil {
		return preparedSnapshotWrite{}, errors.New("PostgreSQL snapshot read failed")
	}
	if !memoryrepo.SupportsSnapshotVersion(prepared.version) {
		return preparedSnapshotWrite{}, errors.New("unsupported PostgreSQL snapshot version")
	}
	state, err := decodeReadSnapshot(raw, prepared.version, uint64(prepared.revision))
	if err != nil {
		return preparedSnapshotWrite{}, err
	}
	prepared.state = state
	return prepared, nil
}

func lockSnapshotWrite(ctx context.Context, tx *sql.Tx, prepared preparedSnapshotWrite) (*memoryrepo.Store, error) {
	var version int
	var revision int64
	var transactionID string
	if err := tx.QueryRowContext(ctx, `SELECT format_version, revision, xmin::text
 FROM nyvp_control_snapshots WHERE singleton=true FOR UPDATE`).Scan(&version, &revision, &transactionID); err != nil {
		return nil, errors.New("PostgreSQL snapshot lock failed")
	}
	if version == prepared.version && revision == prepared.revision && transactionID == prepared.transactionID {
		return prepared.state, nil
	}
	// Another writer won while decoding. Re-read once under the lock rather
	// than replaying arbitrary business callbacks or retrying without a bound.
	// Traffic and protocol-health overlays are always merged after this lock,
	// even on the fast path, so their independent updates cannot be lost.
	current, err := prepareSnapshotWrite(ctx, tx)
	if err != nil {
		return nil, err
	}
	return current.state, nil
}

func (s *Store) lockReadSnapshot(ctx context.Context, tx *sql.Tx, cached snapshotReadCache) (snapshotReadCache, error) {
	var version int
	var revision int64
	var transactionID string
	if err := tx.QueryRowContext(ctx, `SELECT format_version, revision, xmin::text
 FROM nyvp_control_snapshots WHERE singleton=true FOR UPDATE`).Scan(&version, &revision, &transactionID); err != nil {
		return snapshotReadCache{}, errors.New("protocol health snapshot lock failed")
	}
	if version == cached.version && revision == cached.revision && transactionID == cached.transactionID {
		return cached, nil
	}
	current, _, err := s.validatedReadSnapshot(ctx, tx)
	return current, err
}
