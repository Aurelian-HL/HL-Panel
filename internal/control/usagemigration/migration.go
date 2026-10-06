// Package usagemigration applies and verifies the normalized usage-ledger
// PostgreSQL schema without involving the control-plane snapshot store.
package usagemigration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	Version int64 = 3
	Name          = "usage_ledger"

	// One database-wide key serializes every usage-ledger schema operation.
	advisoryLockKey int64 = 18534598210003
	stateTable            = "nyvp_schema_migrations"
)

// These constants are intentionally updated only after the checked-in
// migration files are final. The embedded bytes are rejected when they drift.
const (
	UpSHA256   = "ace2972c409f486e7e24520613bb5fa38bc7d3e1edd8b988a923d5839889229a"
	DownSHA256 = "5b27c5cbf02c10ba49595382534af7f55e8c33107c0fd59ced5b7a384649f6b6"
)

var (
	ErrNotApplied         = errors.New("usage migration is not applied")
	ErrChecksumMismatch   = errors.New("usage migration checksum mismatch")
	ErrSchemaDrift        = errors.New("usage migration schema drift")
	ErrUnmanagedObjects   = errors.New("unmanaged usage migration objects exist")
	ErrRollbackDataExists = errors.New("usage ledger contains data; rollback requires force")
)

//go:embed sql/000003_usage_ledger.up.sql
var embeddedUp []byte

//go:embed sql/000003_usage_ledger.down.sql
var embeddedDown []byte

type State string

const (
	StatePending   State = "pending"
	StateApplied   State = "applied"
	StateUnmanaged State = "unmanaged"
	StateDrifted   State = "drifted"
)

type Status struct {
	State   State
	Version int64
	Detail  string
}

type ApplyResult struct {
	Applied bool
	Version int64
}

type RollbackResult struct {
	RolledBack bool
	Version    int64
}

type Runner struct {
	db       *sql.DB
	upBody   string
	downBody string
	now      func() time.Time
}

func New(db *sql.DB) (*Runner, error) {
	if db == nil {
		return nil, errors.New("usage migration database is required")
	}
	upBody, err := prepareMigration(embeddedUp, UpSHA256)
	if err != nil {
		return nil, fmt.Errorf("validate embedded usage up migration: %w", err)
	}
	downBody, err := prepareMigration(embeddedDown, DownSHA256)
	if err != nil {
		return nil, fmt.Errorf("validate embedded usage down migration: %w", err)
	}
	return &Runner{db: db, upBody: upBody, downBody: downBody, now: time.Now}, nil
}

func prepareMigration(raw []byte, expectedSHA256 string) (string, error) {
	digest := fmt.Sprintf("%x", sha256.Sum256(raw))
	if digest != expectedSHA256 {
		return "", fmt.Errorf("%w: expected %s, got %s", ErrChecksumMismatch, expectedSHA256, digest)
	}
	text := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(text, "BEGIN;") || !strings.HasSuffix(text, "COMMIT;") {
		return "", errors.New("embedded migration must have an explicit BEGIN/COMMIT wrapper")
	}
	body := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(text, "BEGIN;")), "COMMIT;"))
	if body == "" {
		return "", errors.New("embedded migration body is empty")
	}
	return body, nil
}

func (runner *Runner) Status(ctx context.Context) (Status, error) {
	tx, err := runner.beginLocked(ctx)
	if err != nil {
		return Status{}, err
	}
	defer tx.Rollback()

	kind, err := relationKind(ctx, tx, stateTable)
	if err != nil {
		return Status{}, err
	}
	if kind == "" {
		present, err := migrationRelationsPresent(ctx, tx)
		if err != nil {
			return Status{}, err
		}
		state := StatePending
		detail := ""
		if present {
			state = StateUnmanaged
			detail = ErrUnmanagedObjects.Error()
		}
		if err := tx.Commit(); err != nil {
			return Status{}, fmt.Errorf("commit usage migration status: %w", err)
		}
		return Status{State: state, Version: Version, Detail: detail}, nil
	}
	if kind != "r" && kind != "p" {
		return runner.commitStatus(tx, Status{State: StateDrifted, Version: Version, Detail: "migration state object is not a table"})
	}

	record, found, err := readState(ctx, tx)
	if err != nil {
		return Status{}, driftf("read migration state: %v", err)
	}
	if !found {
		present, err := migrationRelationsPresent(ctx, tx)
		if err != nil {
			return Status{}, err
		}
		state := StatePending
		detail := ""
		if present {
			state = StateUnmanaged
			detail = ErrUnmanagedObjects.Error()
		}
		return runner.commitStatus(tx, Status{State: state, Version: Version, Detail: detail})
	}
	if err := validateState(record); err != nil {
		return runner.commitStatus(tx, Status{State: StateDrifted, Version: Version, Detail: err.Error()})
	}
	if err := verifySchema(ctx, tx); err != nil {
		if errors.Is(err, ErrSchemaDrift) {
			return runner.commitStatus(tx, Status{State: StateDrifted, Version: Version, Detail: err.Error()})
		}
		return Status{}, err
	}
	return runner.commitStatus(tx, Status{State: StateApplied, Version: Version})
}

func (runner *Runner) Apply(ctx context.Context) (ApplyResult, error) {
	tx, err := runner.beginLocked(ctx)
	if err != nil {
		return ApplyResult{}, err
	}
	defer tx.Rollback()

	if err := createStateTable(ctx, tx); err != nil {
		return ApplyResult{}, err
	}
	record, found, err := readState(ctx, tx)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("read usage migration state: %w", err)
	}
	if found {
		if err := validateState(record); err != nil {
			return ApplyResult{}, err
		}
		if err := verifySchema(ctx, tx); err != nil {
			return ApplyResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return ApplyResult{}, fmt.Errorf("commit existing usage migration: %w", err)
		}
		return ApplyResult{Version: Version}, nil
	}
	present, err := migrationRelationsPresent(ctx, tx)
	if err != nil {
		return ApplyResult{}, err
	}
	if present {
		return ApplyResult{}, ErrUnmanagedObjects
	}
	if _, err := tx.ExecContext(ctx, runner.upBody); err != nil {
		return ApplyResult{}, fmt.Errorf("execute usage up migration: %w", err)
	}
	if err := verifySchema(ctx, tx); err != nil {
		return ApplyResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO nyvp_schema_migrations
		(version,name,up_sha256,down_sha256,applied_at) VALUES ($1,$2,$3,$4,$5)`,
		Version, Name, UpSHA256, DownSHA256, runner.now().UTC()); err != nil {
		return ApplyResult{}, fmt.Errorf("record usage migration state: %w", err)
	}
	record, found, err = readState(ctx, tx)
	if err != nil || !found {
		return ApplyResult{}, driftf("new migration state is unreadable")
	}
	if err := validateState(record); err != nil {
		return ApplyResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ApplyResult{}, fmt.Errorf("commit usage up migration: %w", err)
	}
	return ApplyResult{Applied: true, Version: Version}, nil
}

func (runner *Runner) Verify(ctx context.Context) error {
	tx, err := runner.beginLocked(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	kind, err := relationKind(ctx, tx, stateTable)
	if err != nil {
		return err
	}
	if kind != "r" && kind != "p" {
		return ErrNotApplied
	}
	record, found, err := readState(ctx, tx)
	if err != nil {
		return driftf("read migration state: %v", err)
	}
	if !found {
		return ErrNotApplied
	}
	if err := validateState(record); err != nil {
		return err
	}
	if err := verifySchema(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit usage migration verification: %w", err)
	}
	return nil
}

func (runner *Runner) Rollback(ctx context.Context, force bool) (RollbackResult, error) {
	tx, err := runner.beginLocked(ctx)
	if err != nil {
		return RollbackResult{}, err
	}
	defer tx.Rollback()
	kind, err := relationKind(ctx, tx, stateTable)
	if err != nil {
		return RollbackResult{}, err
	}
	if kind != "r" && kind != "p" {
		return RollbackResult{}, ErrNotApplied
	}
	record, found, err := readState(ctx, tx)
	if err != nil {
		return RollbackResult{}, driftf("read migration state: %v", err)
	}
	if !found {
		return RollbackResult{}, ErrNotApplied
	}
	if err := validateState(record); err != nil {
		return RollbackResult{}, err
	}
	if !force {
		hasData, err := rollbackProtectedDataExists(ctx, tx)
		if err != nil {
			return RollbackResult{}, err
		}
		if hasData {
			return RollbackResult{}, ErrRollbackDataExists
		}
	}
	if _, err := tx.ExecContext(ctx, runner.downBody); err != nil {
		return RollbackResult{}, fmt.Errorf("execute usage down migration: %w", err)
	}
	present, err := migrationRelationsPresent(ctx, tx)
	if err != nil {
		return RollbackResult{}, err
	}
	if present {
		return RollbackResult{}, driftf("down migration left usage relations behind")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM nyvp_schema_migrations WHERE version=$1`, Version); err != nil {
		return RollbackResult{}, fmt.Errorf("remove usage migration state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return RollbackResult{}, fmt.Errorf("commit usage down migration: %w", err)
	}
	return RollbackResult{RolledBack: true, Version: Version}, nil
}

func (runner *Runner) beginLocked(ctx context.Context) (*sql.Tx, error) {
	tx, err := runner.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin usage migration transaction: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, advisoryLockKey); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("lock usage migration: %w", err)
	}
	return tx, nil
}

func (runner *Runner) commitStatus(tx *sql.Tx, status Status) (Status, error) {
	if err := tx.Commit(); err != nil {
		return Status{}, fmt.Errorf("commit usage migration status: %w", err)
	}
	return status, nil
}

type stateRecord struct {
	Name       string
	UpSHA256   string
	DownSHA256 string
}

func createStateTable(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS nyvp_schema_migrations (
		version bigint PRIMARY KEY,
		name text NOT NULL UNIQUE,
		up_sha256 text NOT NULL,
		down_sha256 text NOT NULL,
		applied_at timestamptz NOT NULL,
		CONSTRAINT nyvp_schema_migrations_version_positive CHECK (version > 0),
		CONSTRAINT nyvp_schema_migrations_up_sha256 CHECK (up_sha256 ~ '^[0-9a-f]{64}$'),
		CONSTRAINT nyvp_schema_migrations_down_sha256 CHECK (down_sha256 ~ '^[0-9a-f]{64}$')
	)`)
	if err != nil {
		return fmt.Errorf("create usage migration state table: %w", err)
	}
	return nil
}

func readState(ctx context.Context, tx *sql.Tx) (stateRecord, bool, error) {
	var record stateRecord
	err := tx.QueryRowContext(ctx, `SELECT name,up_sha256,down_sha256
		FROM nyvp_schema_migrations WHERE version=$1`, Version).Scan(&record.Name, &record.UpSHA256, &record.DownSHA256)
	if errors.Is(err, sql.ErrNoRows) {
		return stateRecord{}, false, nil
	}
	if err != nil {
		return stateRecord{}, false, err
	}
	return record, true, nil
}

func validateState(record stateRecord) error {
	if record.Name != Name || record.UpSHA256 != UpSHA256 || record.DownSHA256 != DownSHA256 {
		return fmt.Errorf("%w: version %d state does not match embedded migration", ErrChecksumMismatch, Version)
	}
	return nil
}

func driftf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrSchemaDrift, fmt.Sprintf(format, args...))
}
