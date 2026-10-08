// Package postgressnapshot provides bounded PostgreSQL-backed transactional
// snapshots for a single-instance evaluation deployment. It is not the planned
// normalized relational repository and is not a high-throughput data store.
package postgressnapshot

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const operationTimeout = 10 * time.Second

// Writes always load private state inside a row-locked database transaction.
// Reads reuse a validated snapshot only while its database row identity matches.
// Database errors are intentionally redacted to avoid leaking DSNs or payloads.
type Store struct {
	db             *sql.DB
	desiredChanges generations.ChangeSignals
	readMu         sync.Mutex
	readCache      snapshotReadCache
}

type snapshotReadCache struct {
	version       int
	revision      int64
	transactionID string
	state         *memoryrepo.Store
}

func (s *Store) readSnapshot(ctx context.Context, tx *sql.Tx) (*memoryrepo.Store, error) {
	// Coalesce concurrent cold reads. PostgreSQL still validates every request;
	// xmin also detects restores or external repairs that do not bump revision.
	s.readMu.Lock()
	defer s.readMu.Unlock()
	cached := s.readCache
	var next snapshotReadCache
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT format_version, revision, xmin::text,
 CASE WHEN format_version=$1 AND revision=$2 AND xmin::text=$3 THEN NULL ELSE payload END
 FROM nyvp_control_snapshots WHERE singleton=true`, cached.version, cached.revision, cached.transactionID).
		Scan(&next.version, &next.revision, &next.transactionID, &raw)
	if err != nil {
		return nil, errors.New("PostgreSQL snapshot read failed")
	}
	if !memoryrepo.SupportsSnapshotVersion(next.version) {
		return nil, errors.New("unsupported PostgreSQL snapshot version")
	}
	if raw == nil && cached.state != nil {
		return cached.state, nil
	}
	next.state, err = memoryrepo.DecodeSnapshotAtVersion(raw, next.version)
	if err != nil {
		return nil, err
	}
	next.state.SetPersistenceRevision(uint64(next.revision))
	s.readCache = next
	return next.state, nil
}

func (s *Store) DesiredConfigChanges(nodeID string) <-chan struct{} {
	return s.desiredChanges.ForNode(nodeID)
}

func Open(ctx context.Context, databaseURL string, bootstrap auth.Administrator) (*Store, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, errors.New("invalid PostgreSQL connection configuration")
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)
	store := &Store{db: db}
	if err := store.initialize(ctx, bootstrap); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) initialize(parent context.Context, bootstrap auth.Administrator) error {
	ctx, cancel := context.WithTimeout(parent, operationTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("PostgreSQL connection unavailable")
	}
	defer tx.Rollback()
	// Serialize bootstrap and table creation across accidental concurrent starts.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(1853459821)`); err != nil {
		return errors.New("PostgreSQL initialization lock failed")
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS nyvp_control_snapshots (
		singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
		format_version integer NOT NULL,
		revision bigint NOT NULL CHECK (revision > 0),
		payload bytea NOT NULL CHECK (octet_length(payload) BETWEEN 1 AND 16777216),
		updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
	)`); err != nil {
		return errors.New("PostgreSQL snapshot table initialization failed")
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM nyvp_control_snapshots`).Scan(&count); err != nil {
		return errors.New("PostgreSQL snapshot lookup failed")
	}
	if count == 0 {
		if bootstrap.ID == "" || bootstrap.Username == "" || auth.ValidatePasswordHash(bootstrap.PasswordHash) != nil {
			return errors.New("valid bootstrap administrator required for empty store")
		}
		raw, err := memoryrepo.New(bootstrap).EncodeSnapshot()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO nyvp_control_snapshots (singleton, format_version, revision, payload) VALUES (true, $1, 1, $2)`, memoryrepo.SnapshotVersion, raw); err != nil {
			return errors.New("PostgreSQL initial snapshot write failed")
		}
	}
	if err := initializeAuditJournal(ctx, tx); err != nil {
		return err
	}
	if err := migrationTables(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return errors.New("PostgreSQL initialization commit failed")
	}
	return nil
}

func readState(ctx context.Context, tx *sql.Tx, lock bool) (*memoryrepo.Store, error) {
	query := `SELECT format_version, revision, payload FROM nyvp_control_snapshots WHERE singleton = true`
	if lock {
		query += ` FOR UPDATE`
	}
	var version int
	var revision int64
	var raw []byte
	if err := tx.QueryRowContext(ctx, query).Scan(&version, &revision, &raw); err != nil {
		return nil, errors.New("PostgreSQL snapshot read failed")
	}
	if !memoryrepo.SupportsSnapshotVersion(version) {
		return nil, errors.New("unsupported PostgreSQL snapshot version")
	}
	state, err := memoryrepo.DecodeSnapshotAtVersion(raw, version)
	if err == nil {
		state.SetPersistenceRevision(uint64(revision))
	}
	return state, err
}

func transact[T any](parent context.Context, s *Store, write bool, operation func(*memoryrepo.Store) (T, error)) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(parent, operationTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: !write})
	if err != nil {
		return zero, errors.New("PostgreSQL transaction unavailable")
	}
	defer tx.Rollback()
	var state *memoryrepo.Store
	if write {
		state, err = readState(ctx, tx, true)
	} else {
		state, err = s.readSnapshot(ctx, tx)
	}
	if err != nil {
		return zero, err
	}
	var before map[string]int64
	if write {
		before = state.DesiredGenerationIndex()
	}
	result, err := operation(state)
	if err != nil {
		return result, err
	}
	if write {
		if err := appendAuditJournal(ctx, tx, state.AuditEvents()); err != nil {
			return zero, err
		}
		raw, err := state.EncodeSnapshotWithAudit(nil)
		if err != nil {
			return zero, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET payload = $1, format_version = $2, revision = revision + 1, updated_at = clock_timestamp() WHERE singleton = true`, raw, memoryrepo.SnapshotVersion); err != nil {
			return zero, errors.New("PostgreSQL snapshot write failed")
		}
	}
	if err := tx.Commit(); err != nil {
		return zero, errors.New("PostgreSQL transaction commit failed")
	}
	if write {
		after := state.DesiredGenerationIndex()
		for id, generation := range after {
			if generation != before[id] {
				s.desiredChanges.Notify(id)
			}
		}
		for id := range before {
			if _, exists := after[id]; !exists {
				s.desiredChanges.Notify(id)
			}
		}
	}
	return result, nil
}

func mutate(ctx context.Context, s *Store, operation func(*memoryrepo.Store) error) error {
	_, err := transact(ctx, s, true, func(state *memoryrepo.Store) (struct{}, error) { return struct{}{}, operation(state) })
	return err
}
