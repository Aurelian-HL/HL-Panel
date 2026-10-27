package postgressnapshot

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

// Opt-in local measurement. The same fixture and request batch can be run on
// either side of a persistence change; timings are not correctness assertions.
func TestContendedSnapshotWritesMeasurement(t *testing.T) {
	if os.Getenv("NYVP_TEST_SNAPSHOT_WRITE_PERFORMANCE") != "true" {
		t.Skip("set NYVP_TEST_SNAPSHOT_WRITE_PERFORMANCE=true for contention measurements")
	}
	ctx := context.Background()
	admin := bootstrapAdministrator(t)
	s, err := Open(ctx, isolatedDatabaseURL(t), admin)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	state := memoryrepo.New(admin)
	now := time.Now().UTC()
	for i := range 8000 {
		hash := fmt.Sprintf("%064x", i+1)
		if err := state.CreateSession(ctx, auth.Session{ID: hash, AdminID: admin.ID, TokenHash: hash, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, audit.Event{}); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := encodeHotSnapshot(state)
	if err != nil {
		t.Fatal(err)
	}
	const writers = 8
	for sample := range 4 {
		if _, err := s.db.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET payload=$1,revision=revision+1 WHERE singleton=true`, raw); err != nil {
			t.Fatal(err)
		}
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		poolBefore := s.db.Stats()
		start := make(chan struct{})
		type outcome struct {
			duration time.Duration
			err      error
		}
		results := make(chan outcome, writers)
		var ready sync.WaitGroup
		ready.Add(writers)
		for i := range writers {
			go func() {
				ready.Done()
				<-start
				at := time.Now()
				hash := fmt.Sprintf("%064x", 100000+sample*writers+i)
				calls := 0
				err := mutate(ctx, s, func(current *memoryrepo.Store) error {
					calls++
					return current.CreateSession(ctx, auth.Session{ID: hash, AdminID: admin.ID, TokenHash: hash, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, audit.Event{ID: hash})
				})
				if err == nil && calls != 1 {
					err = fmt.Errorf("callback ran %d times", calls)
				}
				results <- outcome{time.Since(at), err}
			}()
		}
		ready.Wait()
		started := time.Now()
		close(start)
		var sum, maximum time.Duration
		for range writers {
			r := <-results
			if r.err != nil {
				t.Error(r.err)
			}
			sum += r.duration
			if r.duration > maximum {
				maximum = r.duration
			}
		}
		elapsed := time.Since(started)
		runtime.ReadMemStats(&after)
		poolAfter := s.db.Stats()
		t.Logf("SNAPSHOT_CONTENTION sample=%d bytes=%d writers=%d batch_ms=%.3f mean_ms=%.3f max_ms=%.3f allocated_bytes=%d allocations=%d pool_waits=%d", sample, len(raw), writers, float64(elapsed.Microseconds())/1000, float64(sum.Microseconds())/1000/writers, float64(maximum.Microseconds())/1000, after.TotalAlloc-before.TotalAlloc, after.Mallocs-before.Mallocs, poolAfter.WaitCount-poolBefore.WaitCount)
		if t.Failed() {
			t.FailNow()
		}
	}
}
