package postgressnapshot

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
)

func overlayReadTransaction(t *testing.T, s *Store) *sql.Tx {
	t.Helper()
	tx, err := s.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

func seedForwardingOverlayPair(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO hl_panel_rule_traffic VALUES('rule',1,false,'',10,now())`); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(gatewaymembership.ProtocolObservation{RuleID: "rule", NodeID: "node", RuleRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO hl_panel_protocol_health VALUES('rule','node',$1)`, raw); err != nil {
		t.Fatal(err)
	}
}

func TestForwardingOverlaysTransactionCacheAndRollback(t *testing.T) {
	s := openHealthWriteStore(t)
	seedForwardingOverlayPair(t, s)
	ctx := context.Background()
	read := func(tx *sql.Tx, wantTraffic int64, wantHealth uint64) {
		t.Helper()
		traffic, health, err := s.forwardingOverlays(ctx, tx)
		if err != nil || len(traffic) != 1 || len(health) != 1 {
			t.Fatalf("read lengths=%d/%d, err=%v", len(traffic), len(health), err)
		}
		if traffic[0].State.TrafficUsedBytes != wantTraffic || uint64(health[0].RuleRevision) != wantHealth {
			t.Fatalf("values=%d/%d, want=%d/%d", traffic[0].State.TrafficUsedBytes, health[0].RuleRevision, wantTraffic, wantHealth)
		}
		traffic[0].State.TrafficUsedBytes = -1
		health[0].RuleRevision = 99 // Returned slices cannot contaminate cache.
	}
	old := overlayReadTransaction(t, s)
	read(old, 10, 1)
	read(old, 10, 1)
	s.projectionMu.RLock()
	originalHealth := &s.projections.health[0]
	s.projectionMu.RUnlock()
	// Independent connections invalidate just the changed projection.
	if _, err := s.db.Exec(`UPDATE hl_panel_rule_traffic SET used_bytes=20`); err != nil {
		t.Fatal(err)
	}
	current := overlayReadTransaction(t, s)
	read(current, 20, 1)
	s.projectionMu.RLock()
	sameHealth := originalHealth == &s.projections.health[0]
	currentTraffic := &s.projections.ruleTraffic[0]
	s.projectionMu.RUnlock()
	if !sameHealth {
		t.Fatal("traffic-only write reloaded unchanged health cache")
	}
	raw, _ := json.Marshal(gatewaymembership.ProtocolObservation{RuleID: "rule", NodeID: "node", RuleRevision: 2})
	if _, err := s.db.Exec(`UPDATE hl_panel_protocol_health SET payload=$1`, raw); err != nil {
		t.Fatal(err)
	}
	latest := overlayReadTransaction(t, s)
	read(latest, 20, 2)
	s.projectionMu.RLock()
	sameTraffic := currentTraffic == &s.projections.ruleTraffic[0]
	s.projectionMu.RUnlock()
	if !sameTraffic {
		t.Fatal("health-only write reloaded unchanged traffic cache")
	}
	read(old, 10, 1)
	read(current, 20, 1)
	read(latest, 20, 2) // Old readers cannot overwrite the newer cache.
	_ = old.Rollback()
	_ = current.Rollback()
	_ = latest.Rollback()
	writer, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	if _, err := writer.Exec(`UPDATE hl_panel_rule_traffic SET used_bytes=30`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(`DELETE FROM hl_panel_protocol_health`); err != nil {
		t.Fatal(err)
	}
	if err := writer.Rollback(); err != nil {
		t.Fatal(err)
	}
	read(overlayReadTransaction(t, s), 20, 2)
}

func TestForwardingOverlaysMissingMarkersFailClosed(t *testing.T) {
	for _, marker := range []string{ruleTrafficProjection, healthProjection} {
		t.Run(marker, func(t *testing.T) {
			s := openHealthWriteStore(t)
			seedForwardingOverlayPair(t, s)
			tx := overlayReadTransaction(t, s)
			if _, _, err := s.forwardingOverlays(context.Background(), tx); err != nil {
				t.Fatal(err)
			}
			_ = tx.Rollback()
			if _, err := s.db.Exec(`DELETE FROM hl_panel_projection_revisions WHERE projection=$1`, marker); err != nil {
				t.Fatal(err)
			}
			traffic, health, err := s.forwardingOverlays(context.Background(), overlayReadTransaction(t, s))
			if err == nil || traffic != nil || health != nil {
				t.Fatal("missing revision returned cached or partial projections")
			}
		})
	}
}

func TestForwardingOverlaysDecodeFailureReturnsNoPartialResult(t *testing.T) {
	s := openHealthWriteStore(t)
	seedForwardingOverlayPair(t, s)
	tx := overlayReadTransaction(t, s)
	if _, _, err := s.forwardingOverlays(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback()
	if _, err := s.db.Exec(`UPDATE hl_panel_protocol_health SET payload=$1`, []byte("{")); err != nil {
		t.Fatal(err)
	}
	traffic, health, err := s.forwardingOverlays(context.Background(), overlayReadTransaction(t, s))
	if err == nil || traffic != nil || health != nil {
		t.Fatal("malformed health returned cached or partial projections")
	}
}

func TestForwardingOverlaysGeneralReadAndWriteFailClosed(t *testing.T) {
	for _, write := range []bool{false, true} {
		name := "read"
		if write {
			name = "write"
		}
		t.Run(name, func(t *testing.T) {
			s := openHealthWriteStore(t)
			ctx := context.Background()
			// Warm a complete snapshot and both projection caches first.
			tx := overlayReadTransaction(t, s)
			if _, err := s.readSnapshot(ctx, tx); err != nil {
				t.Fatal(err)
			}
			_ = tx.Rollback()
			before, revision := healthSnapshot(t, s)
			if _, err := s.db.Exec(`DELETE FROM hl_panel_projection_revisions WHERE projection=$1`, healthProjection); err != nil {
				t.Fatal(err)
			}
			tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: !write})
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if write {
				_, err = readState(ctx, tx, true, s)
			} else {
				_, err = s.readSnapshot(ctx, tx)
			}
			if err == nil {
				t.Fatal("general snapshot operation bypassed missing health marker")
			}
			_ = tx.Rollback()
			after, afterRevision := healthSnapshot(t, s)
			if !bytes.Equal(before, after) || revision != afterRevision {
				t.Fatal("failed overlay load changed authoritative snapshot")
			}
		})
	}
}
