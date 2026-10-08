// Package postgressnapshot provides bounded PostgreSQL-backed transactional
// snapshots for a single-instance evaluation deployment. It is not the planned
// normalized relational repository and is not a high-throughput data store.
package postgressnapshot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const operationTimeout = 10 * time.Second

// Store does not cache mutable state: every operation loads from PostgreSQL.
// Database errors are intentionally redacted to avoid leaking DSNs or payloads.
type Store struct {
	db             *sql.DB
	desiredChanges generations.ChangeSignals
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
	if _, err := readState(ctx, tx, false); err != nil {
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
	query := `SELECT format_version, payload FROM nyvp_control_snapshots WHERE singleton = true`
	if lock {
		query += ` FOR UPDATE`
	}
	var version int
	var raw []byte
	if err := tx.QueryRowContext(ctx, query).Scan(&version, &raw); err != nil {
		return nil, errors.New("PostgreSQL snapshot read failed")
	}
	if !memoryrepo.SupportsSnapshotVersion(version) {
		return nil, errors.New("unsupported PostgreSQL snapshot version")
	}
	var header struct{ Version int }
	if json.Unmarshal(raw, &header) != nil || header.Version != version {
		return nil, errors.New("PostgreSQL snapshot version mismatch")
	}
	return memoryrepo.DecodeSnapshot(raw)
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
	state, err := readState(ctx, tx, write)
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
		raw, err := state.EncodeSnapshot()
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
