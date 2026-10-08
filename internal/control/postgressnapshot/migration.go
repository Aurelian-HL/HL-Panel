package postgressnapshot

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/migrationbackup"
)

func migrationTables(ctx context.Context, tx *sql.Tx) error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS hl_panel_migration_runtime (singleton boolean PRIMARY KEY CHECK(singleton), payload bytea NOT NULL CHECK(octet_length(payload) BETWEEN 1 AND 4194304))`,
		`CREATE TABLE IF NOT EXISTS hl_panel_migration_receipts (operation_key text PRIMARY KEY, digest text NOT NULL, recovery_id text NOT NULL, restored_at timestamptz NOT NULL)`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return errors.New("migration metadata unavailable")
		}
	}
	return nil
}
func (s *Store) MigrationRuntime(ctx context.Context) (migrationbackup.RuntimeSecrets, bool, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM hl_panel_migration_runtime WHERE singleton=true`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return migrationbackup.RuntimeSecrets{}, false, nil
	}
	if err != nil {
		return migrationbackup.RuntimeSecrets{}, false, errors.New("migration runtime read failed")
	}
	value, err := migrationbackup.DecodeRuntime(raw)
	return value, err == nil, err
}
func captureMigration(ctx context.Context, tx *sql.Tx) (migrationbackup.State, error) {
	var state migrationbackup.State
	if err := tx.QueryRowContext(ctx, `SELECT payload FROM nyvp_control_snapshots WHERE singleton=true`).Scan(&state.Snapshot); err != nil {
		return state, errors.New("migration snapshot unavailable")
	}
	decoded, err := memoryrepo.DecodeSnapshot(state.Snapshot)
	if err != nil {
		return state, errors.New("migration snapshot invalid")
	}
	events, err := readAuditJournal(ctx, tx)
	if err != nil {
		return state, err
	}
	events = append(events, decoded.AuditEvents()...)
	state.Snapshot, err = decoded.EncodeSnapshotWithAudit(events)
	if err != nil {
		return state, err
	}
	state.Usage = map[string]json.RawMessage{}
	total := len(state.Snapshot)
	for _, table := range migrationbackup.Tables() {
		rows, err := tx.QueryContext(ctx, `SELECT row_to_json(t) FROM `+table+` t`)
		if err != nil {
			return state, errors.New("migration ledger unavailable")
		}
		var buf bytes.Buffer
		buf.WriteByte('[')
		first := true
		for rows.Next() {
			var row []byte
			if err = rows.Scan(&row); err != nil {
				break
			}
			total += len(row) + 1
			if total > migrationbackup.MaxExpandedBytes-(4<<20) {
				err = errors.New("migration ledger exceeds archive limit")
				break
			}
			if !first {
				buf.WriteByte(',')
			}
			buf.Write(row)
			first = false
		}
		rowsErr := rows.Err()
		rows.Close()
		if err != nil {
			return state, err
		}
		if rowsErr != nil {
			return state, errors.New("migration ledger read failed")
		}
		buf.WriteByte(']')
		state.Usage[table] = append([]byte(nil), buf.Bytes()...)
	}
	return state, nil
}
func (s *Store) ExportMigration(parent context.Context) (migrationbackup.State, error) {
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return migrationbackup.State{}, errors.New("migration export unavailable")
	}
	defer tx.Rollback()
	state, err := captureMigration(ctx, tx)
	if err != nil {
		return state, err
	}
	if err = tx.Commit(); err != nil {
		return state, errors.New("migration export failed")
	}
	return state, nil
}
func (s *Store) RestoreMigration(parent context.Context, input migrationbackup.RestoreInput) (migrationbackup.Result, error) {
	var result migrationbackup.Result
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, errors.New("migration restore unavailable")
	}
	defer tx.Rollback()
	// No user data is replaced until every participating store is locked. Normal
	// writes resume against the restored snapshot only after a successful commit.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(18534598210047)`); err != nil {
		return result, errors.New("migration restore lock failed")
	}
	operationKey := input.Key
	var previousDigest string
	err = tx.QueryRowContext(ctx, `SELECT digest,recovery_id,restored_at FROM hl_panel_migration_receipts WHERE operation_key=$1`, operationKey).Scan(&previousDigest, &result.RecoveryID, &result.RestoredAt)
	if err == nil {
		if previousDigest != input.Digest {
			return result, faults.ErrIdempotencyConflict
		}
		result.Replayed = true
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, errors.New("migration receipt lookup failed")
	}
	if _, err = tx.ExecContext(ctx, `LOCK TABLE nyvp_control_snapshots, hl_panel_audit_journal, `+strings.Join(migrationbackup.Tables(), ", ")+` IN ACCESS EXCLUSIVE MODE`); err != nil {
		return result, errors.New("migration business lock failed")
	}
	current, err := readState(ctx, tx, false)
	if err != nil {
		return result, err
	}
	administrator, err := current.AdministratorByID(ctx, input.AdministratorID)
	if err != nil || !bytes.Equal(administrator.PasswordHash, input.AdministratorHash) {
		return result, faults.ErrUnauthorized
	}
	before, err := captureMigration(ctx, tx)
	if err != nil {
		return result, err
	}
	recoveryID, err := input.SaveRecovery(before)
	if err != nil {
		return result, err
	}
	restored, err := memoryrepo.PrepareMigrationRestore(input.Bundle.State.Snapshot, input.Event)
	if err != nil {
		return result, errors.New("migration snapshot validation failed")
	}
	restoredState, err := memoryrepo.DecodeSnapshot(restored)
	if err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM hl_panel_audit_journal`); err != nil {
		return result, errors.New("migration audit replacement failed")
	}
	if err = appendAuditJournal(ctx, tx, restoredState.AuditEvents()); err != nil {
		return result, err
	}
	restored, err = restoredState.EncodeSnapshotWithAudit(nil)
	if err != nil {
		return result, err
	}
	for i := len(migrationbackup.Tables()) - 1; i >= 0; i-- {
		if _, err = tx.ExecContext(ctx, `DELETE FROM `+migrationbackup.Tables()[i]); err != nil {
			return result, errors.New("migration ledger replacement failed")
		}
	}
	for _, table := range migrationbackup.Tables() {
		// Column names come exclusively from the installed, verified target schema.
		columnsRows, e := tx.QueryContext(ctx, `SELECT attname FROM pg_attribute WHERE attrelid=$1::regclass AND attnum>0 AND NOT attisdropped ORDER BY attnum`, table)
		if e != nil {
			return result, errors.New("migration ledger schema unavailable")
		}
		columns := []string{}
		for columnsRows.Next() {
			var column string
			if e = columnsRows.Scan(&column); e != nil {
				break
			}
			columns = append(columns, `"`+strings.ReplaceAll(column, `"`, `""`)+`"`)
		}
		rowsErr := columnsRows.Err()
		columnsRows.Close()
		if e != nil || rowsErr != nil || len(columns) == 0 {
			return result, errors.New("migration ledger schema invalid")
		}
		projection := strings.Join(columns, ",")
		if _, err = tx.ExecContext(ctx, `INSERT INTO `+table+` (`+projection+`) SELECT `+projection+` FROM json_populate_recordset(NULL::`+table+`,$1::json)`, string(input.Bundle.State.Usage[table])); err != nil {
			return result, errors.New("migration ledger data invalid; existing panel data retained")
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET payload=$1,format_version=$2,revision=revision+1,updated_at=clock_timestamp() WHERE singleton=true`, restored, memoryrepo.SnapshotVersion); err != nil {
		return result, errors.New("migration snapshot restore failed")
	}
	runtime, _ := json.Marshal(input.Bundle.Runtime)
	if _, err = tx.ExecContext(ctx, `INSERT INTO hl_panel_migration_runtime(singleton,payload) VALUES(true,$1) ON CONFLICT(singleton) DO UPDATE SET payload=excluded.payload`, runtime); err != nil {
		return result, errors.New("migration runtime restore failed")
	}
	result = migrationbackup.Result{RecoveryID: recoveryID, RestoredAt: time.Now().UTC()}
	if _, err = tx.ExecContext(ctx, `INSERT INTO hl_panel_migration_receipts(operation_key,digest,recovery_id,restored_at) VALUES($1,$2,$3,$4)`, operationKey, input.Digest, result.RecoveryID, result.RestoredAt); err != nil {
		return result, errors.New("migration receipt persistence failed")
	}
	if err = tx.Commit(); err != nil {
		return result, errors.New("migration commit failed")
	}
	for id := range current.DesiredGenerationIndex() {
		s.desiredChanges.Notify(id)
	}
	return result, nil
}
