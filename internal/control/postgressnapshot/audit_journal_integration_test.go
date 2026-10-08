package postgressnapshot

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/usagemigration"
)

func TestAuditJournalUpgradesLegacyHistoryAtomicallyAndPreservesBackups(t *testing.T) {
	ctx := context.Background()
	dsn := isolatedDatabaseURL(t)
	admin := bootstrapAdministrator(t)
	s, err := Open(ctx, dsn, admin)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	legacy := memoryrepo.New(admin)
	for _, id := range []string{"first", "second"} {
		if err := legacy.AppendAudit(ctx, audit.Event{ID: id, Metadata: map[string]any{"name": "历史日志"}}); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := legacy.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	fields["Version"] = json.RawMessage(`17`)
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET payload=$1,format_version=17`, raw); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(ctx, dsn, admin)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if _, err = upgraded.db.ExecContext(ctx, `SELECT 1`); err != nil {
		t.Fatal(err)
	}
	tx, err := upgraded.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := readState(ctx, tx, false)
	_ = tx.Rollback()
	if err != nil || len(projection.AuditEvents()) != 0 {
		t.Fatal("legacy history remained on the hot path", err)
	}
	assertIDs := func(want []string) {
		t.Helper()
		events, err := upgraded.AuditEvents(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, event := range events {
			ids = append(ids, event.ID)
		}
		if !reflect.DeepEqual(ids, want) {
			t.Fatalf("audit order differs: %v", ids)
		}
	}
	assertIDs([]string{"first", "second"})
	if err = upgraded.AppendAudit(ctx, audit.Event{ID: "third"}); err != nil {
		t.Fatal(err)
	}
	err = mutate(ctx, upgraded, func(state *memoryrepo.Store) error {
		_ = state.AppendAudit(ctx, audit.Event{ID: "rolled-back"})
		return faults.ErrConflict
	})
	if !errors.Is(err, faults.ErrConflict) {
		t.Fatal(err)
	}
	assertIDs([]string{"first", "second", "third"})
	if err = upgraded.initialize(ctx, admin); err != nil {
		t.Fatal(err)
	}
	assertIDs([]string{"first", "second", "third"})
	runner, err := usagemigration.New(upgraded.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runner.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	backup, err := upgraded.ExportMigration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := memoryrepo.DecodeSnapshot(backup.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.AuditEvents()) != 3 || decoded.AuditEvents()[0].Metadata["name"] != "历史日志" {
		t.Fatal("backup omitted audit history")
	}
	// A journal failure must roll back the snapshot mutation as well.
	if _, err = upgraded.db.ExecContext(ctx, `ALTER TABLE hl_panel_audit_journal ADD CONSTRAINT reject_test CHECK (false) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	err = upgraded.AppendAudit(ctx, audit.Event{ID: "rejected"})
	if err == nil {
		t.Fatal("journal failure was ignored")
	}
	assertIDs([]string{"first", "second", "third"})
}
