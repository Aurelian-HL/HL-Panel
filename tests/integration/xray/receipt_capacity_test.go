package xray_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hongle/hl-panel/internal/agent/reconciler"
	"github.com/hongle/hl-panel/internal/agent/state"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type receiptCapacityReporter struct {
	count int
}

func (r *receiptCapacityReporter) ReportApplyResult(context.Context, string, agentv1.ApplyResultRequest) error {
	r.count++
	return nil
}

func TestXrayFullReceiptQueuePreservesExistingSessionAndResumesAfterDelivery(t *testing.T) {
	f := newLiveUpdateFixture(t)
	root := t.TempDir()
	path := filepath.Join(root, "state.json")
	store := state.NewAtomicStateStore(path, filepath.Join(root, "configs"))
	r, err := reconciler.New(store, f.a, nil, "test-credential")
	if err != nil {
		t.Fatal(err)
	}
	stable, added := updateRule{id: 1, port: f.ports[0]}, updateRule{id: 2, port: f.ports[1]}
	prepared := f.prepare(stable)
	desired := adapterDesired(t, prepared.Generation, prepared.Bundle.Fragments)
	if err := r.Apply(f.ctx, desired); err != nil {
		t.Fatal(err)
	}
	defer f.monitor(f.dial(stable.port, 'A'))()
	// Simulate a control plane that accepts desired generations but cannot
	// receive terminal results. None of these replays may replace the process.
	for generation := 2; generation <= 16; generation++ {
		desired.Generation = agentv1.NodeConfigGeneration(generation)
		if err := r.Apply(f.ctx, desired); err != nil {
			t.Fatal(err)
		}
	}
	current, err := store.Load()
	if err != nil || current.Pending != nil || len(current.PendingReceipts) != 32 {
		t.Fatalf("receipt backlog fixture is not full: %+v, %v", current, err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f.generation = desired.Generation
	candidate := f.prepare(stable, added)
	next := adapterDesired(t, candidate.Generation, candidate.Bundle.Fragments)
	calls := len(f.runner.calls)
	if err := r.Apply(f.ctx, next); !errors.Is(err, state.ErrApplyReceiptQueueFull) {
		t.Fatalf("full queue did not reject before live mutation: %v", err)
	}
	if len(f.runner.calls) != calls {
		t.Fatalf("capacity rejection invoked Xray API: %v", f.runner.calls[calls:])
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatalf("capacity rejection changed durable state: %v", err)
	}
	f.closed(added.port)
	_ = f.dial(stable.port, 'A')

	// Deliver the backlog through a local reporter, then apply the very same
	// rejected desired configuration while the original socket keeps flowing.
	reporter := &receiptCapacityReporter{}
	r, err = reconciler.New(store, f.a, reporter, "test-credential")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.FlushReceipts(f.ctx); err != nil {
		t.Fatal(err)
	}
	if reporter.count != 32 {
		t.Fatalf("backlog delivery count = %d, want 32", reporter.count)
	}
	if err := r.Apply(f.ctx, next); err != nil {
		t.Fatal(err)
	}
	_ = f.dial(added.port, 'A')
	current, err = store.Load()
	if err != nil || current.Applied == nil || current.Applied.Generation != next.Generation || current.Pending != nil || len(current.PendingReceipts) != 0 {
		t.Fatalf("apply did not resume after receipt delivery: %+v, %v", current, err)
	}
}
