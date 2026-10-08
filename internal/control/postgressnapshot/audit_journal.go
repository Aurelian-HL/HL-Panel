package postgressnapshot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

// Each transaction appends one ordered batch. Legacy history moves as one
// batch, avoiding thousands of inserts during the first upgraded start.
func initializeAuditJournal(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS hl_panel_audit_journal (
	 sequence bigserial PRIMARY KEY, payload bytea NOT NULL
	 CHECK(octet_length(payload) BETWEEN 1 AND 16777216))`); err != nil {
		return errors.New("audit journal initialization failed")
	}
	state, err := readState(ctx, tx, true)
	if err != nil {
		return err
	}
	events := state.AuditEvents()
	if len(events) == 0 {
		return nil
	}
	if err := appendAuditJournal(ctx, tx, events); err != nil {
		return err
	}
	raw, err := state.EncodeSnapshotWithAudit(nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET payload=$1,
	 format_version=$2,revision=revision+1,updated_at=clock_timestamp() WHERE singleton=true`, raw, memoryrepo.SnapshotVersion); err != nil {
		return errors.New("audit journal migration failed")
	}
	return nil
}

func appendAuditJournal(ctx context.Context, tx *sql.Tx, events []audit.Event) error {
	if len(events) == 0 {
		return nil
	}
	raw, err := json.Marshal(events)
	if err != nil {
		return errors.New("audit journal encoding failed")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO hl_panel_audit_journal(payload) VALUES($1)`, raw); err != nil {
		return errors.New("audit journal write failed")
	}
	return nil
}

func readAuditJournal(ctx context.Context, tx *sql.Tx) ([]audit.Event, error) {
	rows, err := tx.QueryContext(ctx, `SELECT payload FROM hl_panel_audit_journal ORDER BY sequence`)
	if err != nil {
		return nil, errors.New("audit journal unavailable")
	}
	defer rows.Close()
	events := []audit.Event{}
	for rows.Next() {
		var raw []byte
		var batch []audit.Event
		if err := rows.Scan(&raw); err != nil || json.Unmarshal(raw, &batch) != nil || batch == nil {
			return nil, errors.New("audit journal invalid")
		}
		events = append(events, batch...)
	}
	if rows.Err() != nil {
		return nil, errors.New("audit journal read failed")
	}
	return events, nil
}
