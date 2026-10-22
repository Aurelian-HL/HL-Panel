package postgressnapshot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
)

type countedProtocolHealthWriter struct {
	tx    *sql.Tx
	calls int
	err   error
}

func (w *countedProtocolHealthWriter) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	w.calls++
	if w.err != nil {
		return nil, w.err
	}
	return w.tx.ExecContext(ctx, query, args...)
}

func syntheticHealthProofs(count int) []gatewaymembership.ProtocolObservation {
	proofs := make([]gatewaymembership.ProtocolObservation, count)
	now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	for i := range proofs {
		proofs[i] = gatewaymembership.ProtocolObservation{
			RuleID: fmt.Sprintf("isolated-rule-%04d", i), NodeID: "loopback-node",
			RuleRevision: 1, NodeConfigGeneration: 1, ConfigSHA256: "synthetic",
			Protocol: "vless-reality", DialHost: "127.0.0.1", VerifiedAt: now,
			LeaseExpiresAt: now.Add(time.Minute),
		}
	}
	return proofs
}

func openHealthWriteStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), isolatedDatabaseURL(t), bootstrapAdministrator(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func readPersistedHealth(t *testing.T, s *Store) []gatewaymembership.ProtocolObservation {
	t.Helper()
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	proofs, err := readProtocolHealthOverlays(context.Background(), tx)
	if err != nil {
		t.Fatal(err)
	}
	return proofs
}

func healthProjectionRevision(t *testing.T, s *Store) int64 {
	t.Helper()
	var revision int64
	if err := s.db.QueryRow(`SELECT revision FROM hl_panel_projection_revisions WHERE projection=$1`, healthProjection).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	return revision
}

func TestProtocolHealthBatchWriteBoundsAndPersistence(t *testing.T) {
	for _, count := range []int{0, 1, 47, 128, 129, 257} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s := openHealthWriteStore(t)
			ctx := context.Background()
			proofs := syntheticHealthProofs(count)
			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			writer := &countedProtocolHealthWriter{tx: tx}
			if err := writeProtocolHealthOverlays(ctx, writer, proofs, make([]error, count)); err != nil {
				t.Fatal(err)
			}
			if writer.calls != (count+127)/128 {
				t.Fatalf("writes=%d for proofs=%d", writer.calls, count)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			actual := readPersistedHealth(t, s)
			if len(actual) != count || (count > 0 && !reflect.DeepEqual(actual, proofs)) {
				t.Fatalf("persisted proofs differ: got %d, want %d", len(actual), count)
			}
		})
	}
}

func TestProtocolHealthBatchWriteLastAcceptedAndTupleKeys(t *testing.T) {
	s := openHealthWriteStore(t)
	proofs := syntheticHealthProofs(2)
	// Equal concatenated strings must remain two distinct tuple keys. Quotes
	// and SQL-like text must be carried only as parameters.
	proofs[0].RuleID, proofs[0].NodeID = "a|b", "c'; SELECT 1; --"
	proofs[1].RuleID, proofs[1].NodeID = "a", "b|c'; SELECT 1; --"
	updated := proofs[0]
	updated.VerifiedAt = updated.VerifiedAt.Add(time.Second)
	updated.LeaseExpiresAt = updated.LeaseExpiresAt.Add(time.Second)
	rejected := updated
	rejected.VerifiedAt = rejected.VerifiedAt.Add(time.Second)
	items := []gatewaymembership.ProtocolObservation{proofs[0], proofs[1], updated, rejected}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	writer := &countedProtocolHealthWriter{tx: tx}
	if err := writeProtocolHealthOverlays(ctx, writer, items, []error{nil, nil, nil, errors.New("rejected")}); err != nil {
		t.Fatal(err)
	}
	if writer.calls != 1 {
		t.Fatalf("writes=%d", writer.calls)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// ORDER BY rule_id,node_id sorts "a" before "a|b".
	if actual := readPersistedHealth(t, s); !reflect.DeepEqual(actual, []gatewaymembership.ProtocolObservation{proofs[1], updated}) {
		t.Fatalf("accepted proof order/tuple identity lost: %+v", actual)
	}
}

func TestProtocolHealthBatchWriteFailureRollsBackAllChunks(t *testing.T) {
	for _, failure := range []string{"second-chunk-sql", "second-chunk-encode"} {
		t.Run(failure, func(t *testing.T) {
			s := openHealthWriteStore(t)
			proofs := syntheticHealthProofs(129)
			if failure == "second-chunk-sql" {
				_, err := s.db.Exec(`CREATE FUNCTION reject_health_chunk() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF NEW.rule_id = 'isolated-rule-0128' THEN RAISE EXCEPTION 'isolated chunk rejection'; END IF; RETURN NEW; END $$;
CREATE TRIGGER reject_health_chunk BEFORE INSERT ON hl_panel_protocol_health FOR EACH ROW EXECUTE FUNCTION reject_health_chunk()`)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				proofs[128].VerifiedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			}
			revision := healthProjectionRevision(t, s)
			tx, err := s.db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			writer := &countedProtocolHealthWriter{tx: tx}
			if err := writeProtocolHealthOverlays(context.Background(), writer, proofs, make([]error, len(proofs))); err == nil {
				t.Fatal("injected failure accepted")
			}
			wantCalls := 2
			if failure == "second-chunk-encode" {
				wantCalls = 1
			}
			if writer.calls != wantCalls {
				t.Fatalf("failure did not follow a successful chunk: calls=%d", writer.calls)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if len(readPersistedHealth(t, s)) != 0 || healthProjectionRevision(t, s) != revision {
				t.Fatal("rolled back chunk or projection revision leaked")
			}
		})
	}
}

func TestProtocolHealthBatchWriteRejectsBeforeSQL(t *testing.T) {
	proofs := syntheticHealthProofs(1)
	for _, tc := range []struct {
		name    string
		results []error
		cancel  bool
	}{
		{"mismatched-results", nil, false},
		{"canceled", []error{nil}, true},
		{"all-rejected", []error{errors.New("rejected")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			writer := &countedProtocolHealthWriter{err: errors.New("unexpected write")}
			err := writeProtocolHealthOverlays(ctx, writer, proofs, tc.results)
			if (tc.name != "all-rejected") != (err != nil) || writer.calls != 0 {
				t.Fatalf("err=%v writes=%d", err, writer.calls)
			}
		})
	}
}

// Frozen pre-batch writer, used only for paired measurements against identical
// PostgreSQL transactions. It is never called by production code.
func sequentialHealthWriteBaseline(ctx context.Context, tx protocolHealthWriter, proofs []gatewaymembership.ProtocolObservation, results []error) error {
	for i, proof := range proofs {
		if results[i] != nil {
			continue
		}
		raw, err := json.Marshal(proof)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO hl_panel_protocol_health(rule_id,node_id,payload) VALUES($1,$2,$3) ON CONFLICT(rule_id,node_id) DO UPDATE SET payload=excluded.payload`, proof.RuleID, proof.NodeID, raw); err != nil {
			return err
		}
	}
	return nil
}
