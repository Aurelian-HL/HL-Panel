package postgressnapshot

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

func TestPreparedSnapshotDoesNotLockAndRevalidatesConcurrentWrites(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, isolatedDatabaseURL(t), bootstrapAdministrator(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, repair := range []bool{false, true} {
		t.Run(fmt.Sprint("same-revision-repair=", repair), func(t *testing.T) {
			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			prepared, err := prepareSnapshotWrite(ctx, tx)
			if err != nil {
				t.Fatal(err)
			}
			// A different connection can acquire NOWAIT after preparation; no
			// row lock may be held during the expensive decode.
			other, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Rollback()
			var revision int64
			if err = other.QueryRowContext(ctx, `SELECT revision FROM nyvp_control_snapshots WHERE singleton=true FOR UPDATE NOWAIT`).Scan(&revision); err != nil {
				t.Fatal("snapshot preparation blocked another writer", err)
			}
			latest, err := prepareSnapshotWrite(ctx, other)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			hash := fmt.Sprintf("%064x", revision+100)
			if repair {
				hash = fmt.Sprintf("%064x", revision+200)
			}
			if err = latest.state.CreateSession(ctx, auth.Session{ID: hash, AdminID: "admin-one", TokenHash: hash, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, audit.Event{ID: hash}); err != nil {
				t.Fatal(err)
			}
			raw, err := encodeHotSnapshot(latest.state)
			if err != nil {
				t.Fatal(err)
			}
			advance := int64(1)
			if repair {
				advance = 0
			}
			if _, err = other.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET payload=$1,revision=revision+$2 WHERE singleton=true`, raw, advance); err != nil {
				t.Fatal(err)
			}
			if err = other.Commit(); err != nil {
				t.Fatal(err)
			}
			state, err := lockSnapshotWrite(ctx, tx, prepared)
			if err != nil {
				t.Fatal(err)
			}
			if state == prepared.state {
				t.Fatal("stale prepared state survived a concurrent write")
			}
			if _, err = state.SessionByTokenHash(ctx, hash, now); err != nil {
				t.Fatal("concurrent write was lost", err)
			}
		})
	}
}

func TestPreparedSnapshotConcurrentMutationsAreNotReplayedOrLost(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, isolatedDatabaseURL(t), bootstrapAdministrator(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const writers = 12
	start := make(chan struct{})
	errors := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			calls := 0
			now := time.Now().UTC()
			hash := fmt.Sprintf("%064x", i+1)
			err := mutate(ctx, s, func(state *memoryrepo.Store) error {
				calls++
				return state.CreateSession(ctx, auth.Session{ID: hash, AdminID: "admin-one", TokenHash: hash, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, audit.Event{ID: hash})
			})
			if err == nil && calls != 1 {
				err = fmt.Errorf("business mutation ran %d times", calls)
			}
			errors <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < writers; i++ {
		if _, err = s.SessionByTokenHash(ctx, fmt.Sprintf("%064x", i+1), time.Now().UTC()); err != nil {
			t.Fatal("committed session missing", err)
		}
	}
	events, err := s.AuditEvents(ctx)
	if err != nil || len(events) != writers {
		t.Fatal("audit events duplicated or lost", len(events), err)
	}
}

func TestPreparedHealthViewRevalidatesBeforeLocking(t *testing.T) {
	ctx := context.Background()
	admin := bootstrapAdministrator(t)
	s, err := Open(ctx, isolatedDatabaseURL(t), admin)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	cached, _, err := s.validatedReadSnapshot(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.UpdateAdministratorPassword(ctx, admin.ID, admin.PasswordHash, admin.PasswordHash, true, audit.Event{ID: "concurrent-admin-edit"}); err != nil {
		t.Fatal(err)
	}
	locked, err := s.lockReadSnapshot(ctx, tx, cached)
	if err != nil {
		t.Fatal(err)
	}
	current, err := locked.identities.AdministratorByID(ctx, admin.ID)
	if err != nil || !current.MustChangePassword || locked.revision <= cached.revision {
		t.Fatal("locked health validation used stale business state", err)
	}
}

func TestConcurrentReadMissesShareOnlyImmutableViews(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, isolatedDatabaseURL(t), bootstrapAdministrator(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const readers = 12
	start := make(chan struct{})
	type result struct {
		cached snapshotReadCache
		state  *memoryrepo.Store
		err    error
	}
	results := make(chan result, readers)
	for i := 0; i < readers; i++ {
		go func() {
			<-start
			tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
			if err != nil {
				results <- result{err: err}
				return
			}
			defer tx.Rollback()
			cached, state, err := s.validatedReadSnapshot(ctx, tx)
			results <- result{cached, state, err}
		}()
	}
	close(start)
	var identities *memoryrepo.IdentityReadView
	privateCount := 0
	for i := 0; i < readers; i++ {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		if identities == nil {
			identities = r.cached.identities
		}
		if identities != r.cached.identities {
			t.Fatal("same MVCC row built duplicate immutable views")
		}
		if r.state != nil {
			privateCount++
			admin, err := r.state.AdministratorByID(ctx, "admin-one")
			if err != nil {
				t.Fatal(err)
			}
			if err = r.state.UpdateAdministratorPassword(ctx, admin.ID, admin.PasswordHash, admin.PasswordHash, true, audit.Event{ID: "private-only"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if privateCount > 1 {
		t.Fatal("decoded mutable state was shared across requests")
	}
	admin, err := identities.AdministratorByID(ctx, "admin-one")
	if err != nil || admin.MustChangePassword {
		t.Fatal("private mutation contaminated the immutable projection", err)
	}
}

// Log synthetic timings, without a machine-dependent speed assertion. The
// baseline uses the previous locked read/decode sequence. Both paths roll back
// and process identical bytes; this is not a production concurrency benchmark.
func TestSnapshotWriteLockWindowMeasurement(t *testing.T) {
	ctx := context.Background()
	admin := bootstrapAdministrator(t)
	s, err := Open(ctx, isolatedDatabaseURL(t), admin)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	state := memoryrepo.New(admin)
	now := time.Now().UTC()
	for i := 0; i < 22000; i++ {
		hash := fmt.Sprintf("%064x", i+1)
		if err = state.CreateSession(ctx, auth.Session{ID: hash, AdminID: admin.ID, TokenHash: hash, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, audit.Event{}); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := encodeHotSnapshot(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET payload=$1,revision=revision+1 WHERE singleton=true`, raw); err != nil {
		t.Fatal(err)
	}
	for iteration := 0; iteration < 4; iteration++ {
		for _, preparedFirst := range []bool{false, true} {
			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			var lockedStart time.Time
			var working *memoryrepo.Store
			if preparedFirst {
				var prepared preparedSnapshotWrite
				prepared, err = prepareSnapshotWrite(ctx, tx)
				if err != nil {
					t.Fatal(err)
				}
				lockedStart = time.Now()
				working, err = lockSnapshotWrite(ctx, tx, prepared)
			} else {
				lockedStart = time.Now()
				var version int
				var revision int64
				var input []byte
				err = tx.QueryRowContext(ctx, `SELECT format_version,revision,payload FROM nyvp_control_snapshots WHERE singleton=true FOR UPDATE`).Scan(&version, &revision, &input)
				if err == nil {
					working, err = decodeReadSnapshot(input, version, uint64(revision))
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = encodeHotSnapshot(working); err != nil {
				t.Fatal(err)
			}
			if err = tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			t.Logf("lock-window bytes=%d prepared=%t iteration=%d held_ms=%.3f total_ms=%.3f", len(raw), preparedFirst, iteration, float64(time.Since(lockedStart).Microseconds())/1000, float64(time.Since(start).Microseconds())/1000)
		}
	}
}
