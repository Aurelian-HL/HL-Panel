package postgressnapshot

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
)

func TestProjectionCacheInvalidatesAcrossInstancesAndKeepsTransactionView(t *testing.T) {
	dsn := isolatedDatabaseURL(t)
	ctx := context.Background()
	store, err := Open(ctx, dsn, bootstrapAdministrator(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO hl_panel_rule_traffic VALUES('rule',1,false,'',10,now())`)
	read := func(tx *sql.Tx, want int64) {
		t.Helper()
		items, err := store.ruleTrafficOverlays(ctx, tx)
		if err != nil || len(items) != 1 || items[0].State.TrafficUsedBytes != want {
			t.Fatalf("traffic = %+v, err=%v; want %d", items, err, want)
		}
		items[0].State.TrafficUsedBytes = -1 // Returned data must be detached.
	}
	begin := func() *sql.Tx {
		t.Helper()
		tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tx.Rollback() })
		return tx
	}
	old := begin()
	read(old, 10)
	read(old, 10)
	// An independent connection writes the projection without changing the
	// business snapshot revision, just as another control-api process does.
	exec(`UPDATE hl_panel_rule_traffic SET used_bytes=20`)
	current := begin()
	read(current, 20)
	read(old, 10) // A slow transaction must not overwrite the newer cache.
	read(current, 20)
	_ = old.Rollback()
	_ = current.Rollback()

	rollback, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rollback.ExecContext(ctx, `UPDATE hl_panel_rule_traffic SET used_bytes=30`); err != nil {
		t.Fatal(err)
	}
	_ = rollback.Rollback()
	tx := begin()
	read(tx, 20)
	_ = tx.Rollback()
	exec(`TRUNCATE hl_panel_rule_traffic`)
	tx = begin()
	if items, err := store.ruleTrafficOverlays(ctx, tx); err != nil || len(items) != 0 {
		t.Fatalf("truncate cache = %+v, %v", items, err)
	}
	_ = tx.Rollback()

	observation := gatewaymembership.ProtocolObservation{RuleID: "rule", NodeID: "node", RuleRevision: 1}
	raw, _ := json.Marshal(observation)
	exec(`INSERT INTO hl_panel_protocol_health VALUES('rule','node',$1)`, raw)
	tx = begin()
	items, err := store.protocolHealthOverlays(ctx, tx)
	if err != nil || len(items) != 1 {
		t.Fatalf("health cache: %v %v", items, err)
	}
	items[0].RuleRevision = 99
	items, err = store.protocolHealthOverlays(ctx, tx)
	if err != nil || items[0].RuleRevision != 1 {
		t.Fatal("health cache contaminated")
	}
	_ = tx.Rollback()
	exec(`DELETE FROM hl_panel_protocol_health`)
	tx = begin()
	if items, err := store.protocolHealthOverlays(ctx, tx); err != nil || len(items) != 0 {
		t.Fatalf("deleted health cached: %v %v", items, err)
	}
}
