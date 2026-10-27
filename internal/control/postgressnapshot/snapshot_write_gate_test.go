package postgressnapshot

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

func TestSnapshotWriteGateCancellationAndReuse(t *testing.T) {
	var gate snapshotWriteGate
	release, err := gate.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		unlock, err := gate.acquire(ctx)
		if unlock != nil {
			unlock()
		}
		result <- err
	}()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Error("canceled waiter acquired gate", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("canceled waiter did not return")
	}
	release()
	if unlock, err := gate.acquire(ctx); !errors.Is(err, context.Canceled) || unlock != nil {
		if unlock != nil {
			unlock()
		}
		t.Fatal("already canceled request acquired gate", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	release, err = gate.acquire(ctx)
	if err != nil {
		t.Fatal("cancellation leaked permit", err)
	}
	release()
}

func TestQueuedSnapshotWritesLeaveReadConnectionAndCancelWithoutMutation(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	admin := bootstrapAdministrator(t)
	s, err := Open(ctx, isolatedDatabaseURL(t), admin, PoolConfig{MaxOpenConns: 2, MaxIdleConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.AdministratorByID(ctx, admin.ID); err != nil {
		t.Fatal(err)
	}
	entered, unblock := make(chan struct{}), make(chan struct{})
	active := make(chan error, 1)
	rollback := errors.New("isolated rollback")
	go func() {
		active <- mutate(ctx, s, func(state *memoryrepo.Store) error {
			close(entered)
			select {
			case <-unblock:
			case <-ctx.Done():
			}
			return rollback
		})
	}()
	defer func() {
		close(unblock)
		if err := <-active; !errors.Is(err, rollback) {
			t.Error("active writer did not roll back", err)
		}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("active writer did not enter callback")
	}
	before := s.db.Stats()
	waiting, cancel := context.WithCancel(ctx)
	defer cancel()
	const writers = 6
	results := make(chan error, writers)
	var ready sync.WaitGroup
	var calls atomic.Int32
	ready.Add(writers)
	for range writers {
		go func() {
			ready.Done()
			results <- mutate(waiting, s, func(state *memoryrepo.Store) error {
				calls.Add(1)
				return state.AppendAudit(waiting, audit.Event{ID: "must-not-run"})
			})
		}()
	}
	ready.Wait()
	readCtx, cancelRead := context.WithTimeout(ctx, time.Second)
	_, readErr := s.AdministratorByID(readCtx, admin.ID)
	cancelRead()
	cancel()
	for range writers {
		if err := <-results; !errors.Is(err, context.Canceled) {
			t.Error("queued write did not cancel before transaction", err)
		}
	}
	if readErr != nil {
		t.Fatal("queued writers blocked authentication", readErr)
	}
	if calls.Load() != 0 {
		t.Fatal("canceled queued request executed its mutation")
	}
	after := s.db.Stats()
	if after.WaitCount != before.WaitCount || after.InUse != 1 {
		t.Fatalf("write queue borrowed read connections: waits=%d active=%d", after.WaitCount-before.WaitCount, after.InUse)
	}
}
