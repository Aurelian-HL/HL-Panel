package postgressnapshot

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

// This opt-in paired measurement covers cached projection reads only, with
// BEGIN/COMMIT reported separately. It does not measure full API or dispatch.
func TestForwardingOverlaysPairedPerformance(t *testing.T) {
	if os.Getenv("NYVP_TEST_OVERLAY_READ_PERFORMANCE") != "true" {
		t.Skip("set NYVP_TEST_OVERLAY_READ_PERFORMANCE=true for paired measurements")
	}
	for _, count := range []int{0, 269, 2000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s := openHealthWriteStore(t)
			ctx := context.Background()
			if _, err := s.db.Exec(`INSERT INTO hl_panel_rule_traffic
				SELECT 'rule-'||i,1,false,'',10,now() FROM generate_series(1,$1::int) i`, count); err != nil {
				t.Fatal(err)
			}
			proofs := syntheticHealthProofs(count)
			payloads := make([]json.RawMessage, len(proofs))
			for i := range proofs {
				raw, err := json.Marshal(proofs[i])
				if err != nil {
					t.Fatal(err)
				}
				payloads[i] = raw
			}
			if count > 0 {
				raw, err := json.Marshal(payloads)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.Exec(`INSERT INTO hl_panel_protocol_health
					SELECT p->>'RuleID',p->>'NodeID',convert_to(p::text,'UTF8') FROM json_array_elements($1::json) p`, string(raw)); err != nil {
					t.Fatal(err)
				}
			}
			measure := func(mode string, sample int) {
				t.Helper()
				const iterations = 32
				var readDuration time.Duration
				start := time.Now()
				for range iterations {
					func() {
						tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
						if err != nil {
							t.Fatal(err)
						}
						defer tx.Rollback()
						readStart := time.Now()
						if mode == "separate" {
							traffic, err := s.ruleTrafficOverlays(ctx, tx)
							if err != nil || len(traffic) != count {
								t.Fatalf("traffic length=%d, err=%v", len(traffic), err)
							}
							health, err := s.protocolHealthOverlays(ctx, tx)
							if err != nil || len(health) != count {
								t.Fatalf("health length=%d, err=%v", len(health), err)
							}
						} else {
							traffic, health, err := s.forwardingOverlays(ctx, tx)
							if err != nil || len(traffic) != count || len(health) != count {
								t.Fatalf("lengths=%d/%d, err=%v", len(traffic), len(health), err)
							}
						}
						readDuration += time.Since(readStart)
						if err := tx.Commit(); err != nil {
							t.Fatal(err)
						}
					}()
				}
				if sample >= 0 {
					t.Logf("OVERLAY_READ_SAMPLE rows=%d mode=%s sample=%d read_ns=%d transaction_ns=%d iterations=%d", count, mode, sample, readDuration.Nanoseconds()/iterations, time.Since(start).Nanoseconds()/iterations, iterations)
				}
			}
			for range 3 {
				measure("separate", -1)
				measure("combined", -1)
			}
			for sample := range 20 {
				if sample%2 == 0 {
					measure("separate", sample)
					measure("combined", sample)
				} else {
					measure("combined", sample)
					measure("separate", sample)
				}
			}
		})
	}
}
