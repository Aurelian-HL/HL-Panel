package postgressnapshot

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"
)

// Explicit opt-in: loopback paired database measurements, not a correctness
// timing threshold. Fixture creation and warm-up are excluded from each sample.
func TestProtocolHealthBatchWritePairedPerformance(t *testing.T) {
	if os.Getenv("NYVP_TEST_HEALTH_WRITE_PERFORMANCE") != "true" {
		t.Skip("set NYVP_TEST_HEALTH_WRITE_PERFORMANCE=true for paired measurements")
	}
	for _, count := range []int{1, 47, 128, 257} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s := openHealthWriteStore(t)
			ctx := context.Background()
			proofs := syntheticHealthProofs(count)
			results := make([]error, count)
			iterations := 8
			if count == 1 {
				iterations = 32
			}
			measure := func(mode string, sample int) {
				t.Helper()
				start := time.Now()
				var lockedDuration time.Duration
				for range iterations {
					lockedDuration += func() time.Duration {
						tx, err := s.db.BeginTx(ctx, nil)
						if err != nil {
							t.Fatal(err)
						}
						defer tx.Rollback()
						var revision int64
						if err := tx.QueryRowContext(ctx, `SELECT revision FROM nyvp_control_snapshots WHERE singleton=true FOR UPDATE`).Scan(&revision); err != nil {
							t.Fatal(err)
						}
						locked := time.Now()
						writer := &countedProtocolHealthWriter{tx: tx}
						write := sequentialHealthWriteBaseline
						if mode == "batched" {
							write = writeProtocolHealthOverlays
						}
						if err := write(ctx, writer, proofs, results); err != nil {
							t.Fatal(err)
						}
						if _, err := tx.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET revision=revision+1,updated_at=clock_timestamp() WHERE singleton=true`); err != nil {
							t.Fatal(err)
						}
						if err := tx.Commit(); err != nil {
							t.Fatal(err)
						}
						end := time.Now()
						wantCalls := count
						if mode == "batched" {
							wantCalls = (count + 127) / 128
						}
						if writer.calls != wantCalls {
							t.Fatalf("writes=%d, want=%d", writer.calls, wantCalls)
						}
						return end.Sub(locked)
					}()
				}
				if sample >= 0 {
					writes := count
					if mode == "batched" {
						writes = (count + 127) / 128
					}
					t.Logf("HEALTH_WRITE_SAMPLE rows=%d mode=%s sample=%d transaction_ns=%d locked_section_ns=%d writes=%d iterations=%d", count, mode, sample, time.Since(start).Nanoseconds()/int64(iterations), lockedDuration.Nanoseconds()/int64(iterations), writes, iterations)
				}
			}
			// Seed identical existing keys before measuring renewal upserts.
			for i := range 3 {
				measure("baseline", -i-1)
				measure("batched", -i-1)
			}
			for sample := range 20 {
				if sample%2 == 0 {
					measure("baseline", sample)
					measure("batched", sample)
				} else {
					measure("batched", sample)
					measure("baseline", sample)
				}
			}
			if !reflect.DeepEqual(readPersistedHealth(t, s), proofs) {
				t.Fatal("measurement changed durable proofs")
			}
		})
	}
}
