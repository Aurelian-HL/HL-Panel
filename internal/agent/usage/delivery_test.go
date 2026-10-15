package usage

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type delayedSender struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	reports []agentv1.UsageReport
	badAck  bool
}

func (s *delayedSender) ReportUsage(ctx context.Context, _ string, r agentv1.UsageReport) (agentv1.UsageAcknowledgement, error) {
	s.mu.Lock()
	s.reports = append(s.reports, r)
	s.mu.Unlock()
	s.once.Do(func() { close(s.started) })
	select {
	case <-ctx.Done():
		return agentv1.UsageAcknowledgement{}, ctx.Err()
	case <-s.release:
	}
	sequence := r.Sequence
	if s.badAck {
		sequence++
	}
	return agentv1.UsageAcknowledgement{NodeID: r.NodeID, BootID: r.BootID, Sequence: sequence}, nil
}

func TestDeliveryAcknowledgesSnapshotWithoutDroppingConcurrentFinalCounters(t *testing.T) {
	for _, badAck := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid-ack", true: "invalid-ack"}[badAck], func(t *testing.T) {
			var clock atomic.Int64
			clock.Store(time.Now().UnixNano())
			now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
			store := NewJournalStore(filepath.Join(t.TempDir(), "usage.json"))
			sender := &delayedSender{started: make(chan struct{}), release: make(chan struct{}), badAck: badAck}
			reporter, err := NewReporter(store, &sourceStub{deltas: []CounterDelta{validDelta()}}, sender, "node-one", "credential", 10, now)
			if err != nil {
				t.Fatal(err)
			}
			clock.Add(int64(time.Second))
			if err := reporter.CollectOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			before, _ := store.Load()
			deliveryDone := make(chan error, 1)
			go func() { deliveryDone <- reporter.DeliverPending(context.Background()) }()
			released := false
			defer func() {
				if !released {
					close(sender.release)
					<-deliveryDone
				}
			}()
			select {
			case <-sender.started:
			case <-time.After(time.Second):
				t.Fatal("delivery never started")
			}
			clock.Add(int64(time.Second))
			collectionDone := make(chan error, 1)
			go func() { collectionDone <- reporter.CollectOnce(context.Background()) }()
			select {
			case err := <-collectionDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("delivery blocked final collection")
			}
			close(sender.release)
			released = true
			err = <-deliveryDone
			if (err != nil) != badAck {
				t.Fatalf("delivery error = %v, invalid ack = %v", err, badAck)
			}
			after, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if badAck {
				if len(after.Pending) != 2 || !reflect.DeepEqual(before.Pending[0], after.Pending[0]) {
					t.Fatal("invalid ack changed durable retry payload")
				}
			} else if len(after.Pending) != 1 || after.Pending[0].Sequence != 2 {
				t.Fatalf("snapshot drain discarded later collection: %#v", after.Pending)
			}
		})
	}
}

func TestConcurrentDeliveryRetriesInOrderOnce(t *testing.T) {
	store := NewJournalStore(filepath.Join(t.TempDir(), "usage.json"))
	current := time.Now().UTC()
	sender := &delayedSender{started: make(chan struct{}), release: make(chan struct{})}
	reporter, err := NewReporter(store, &sourceStub{deltas: []CounterDelta{validDelta()}}, sender, "node-one", "credential", 10, func() time.Time { return current })
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		current = current.Add(time.Second)
		if err := reporter.CollectOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	close(sender.release)
	var done sync.WaitGroup
	done.Add(4)
	for range 4 {
		go func() {
			defer done.Done()
			if err := reporter.DeliverPending(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	done.Wait()
	if len(sender.reports) != 3 {
		t.Fatalf("duplicate sends: %d", len(sender.reports))
	}
	for i, r := range sender.reports {
		if r.Sequence != int64(i+1) {
			t.Fatal("delivery reordered pending reports")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	current = current.Add(time.Second)
	if err := reporter.CollectOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := reporter.DeliverPending(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	journal, _ := store.Load()
	if len(journal.Pending) != 1 || journal.Pending[0].Sequence != 4 {
		t.Fatal("canceled delivery lost unsent counters")
	}
}
