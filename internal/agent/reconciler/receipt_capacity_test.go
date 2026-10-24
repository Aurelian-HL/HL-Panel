package reconciler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/engine"
	"github.com/hongle/hl-panel/internal/agent/state"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestFullReceiptQueueRejectsBeforeEngineCommitAndRecoversAfterDelivery(t *testing.T) {
	dir := t.TempDir()
	store := state.NewAtomicStateStore(filepath.Join(dir, "state.json"), filepath.Join(dir, "configs"))
	active := filepath.Join(dir, "active.json")
	file, err := engine.NewDryRunFileAdapter(filepath.Join(dir, "staged.json"), active)
	if err != nil {
		t.Fatal(err)
	}
	adapter := &recordingLiveAdapter{DryRunFileAdapter: file}
	r, err := New(store, adapter, nil, "test-credential")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	desired := testDesired(1, `{"schema_version":1,"fragments":[]}`)
	desired.Generation = 0
	for gen := 1; gen <= 16; gen++ {
		desired.Generation++
		if err := r.Apply(ctx, desired); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.ReadFile(active)
	if err != nil {
		t.Fatal(err)
	}
	stateBefore, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Replaying an applied generation consumes no new receipts.
	if err := r.Apply(ctx, desired); err != nil {
		t.Fatal(err)
	}
	desired.Generation++
	if err := r.Apply(ctx, desired); !errors.Is(err, state.ErrApplyReceiptQueueFull) {
		t.Fatalf("new apply must reject the full receipt queue: %v", err)
	}
	if adapter.commits != 16 || !adapter.running {
		t.Fatalf("full receipt queue changed engine before rejection: commits=%d running=%v", adapter.commits, adapter.running)
	}
	after, err := os.ReadFile(active)
	if err != nil || string(before) != string(after) {
		t.Fatalf("full queue changed active engine file: %v", err)
	}
	stateAfter, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil || string(stateBefore) != string(stateAfter) {
		t.Fatalf("full queue changed applied/pending state: %v", err)
	}
	reloaded, err := New(store, adapter, &recordingReporter{}, "test-credential")
	if err != nil {
		t.Fatal(err)
	}
	if err := reloaded.FlushReceipts(ctx); err != nil {
		t.Fatal(err)
	}
	if err := reloaded.Apply(ctx, desired); err != nil {
		t.Fatal(err)
	}
	current, err := store.Load()
	if err != nil || current.Applied == nil || current.Applied.Generation != desired.Generation || len(current.PendingReceipts) != 0 || adapter.commits != 17 {
		t.Fatalf("apply did not recover after receipt delivery: state=%+v commits=%d error=%v", current, adapter.commits, err)
	}
}

func TestFullReceiptQueueDoesNotPreventDeadEngineRecovery(t *testing.T) {
	dir := t.TempDir()
	store := state.NewAtomicStateStore(filepath.Join(dir, "state.json"), filepath.Join(dir, "configs"))
	file, err := engine.NewDryRunFileAdapter(filepath.Join(dir, "staged.json"), filepath.Join(dir, "active.json"))
	if err != nil {
		t.Fatal(err)
	}
	first := &recordingLiveAdapter{DryRunFileAdapter: file}
	r, err := New(store, first, nil, "test-credential")
	if err != nil {
		t.Fatal(err)
	}
	desired := testDesired(1, `{"schema_version":1,"fragments":[{"group_id":"test","group_generation":1,"engine":"gost","config":{"services":[]}}]}`)
	for gen := 1; gen <= 16; gen++ {
		desired.Generation = agentv1.NodeConfigGeneration(gen)
		if err := r.Apply(context.Background(), desired); err != nil {
			t.Fatal(err)
		}
	}
	before, err := store.Load()
	if err != nil || len(before.PendingReceipts) != 32 {
		t.Fatalf("full queue fixture = %+v, %v", before, err)
	}
	restarted := &recordingLiveAdapter{DryRunFileAdapter: file}
	r, err = New(store, restarted, nil, "test-credential")
	if err != nil {
		t.Fatal(err)
	}
	if restored, err := r.EnsureApplied(context.Background()); err != nil || !restored || !restarted.running || restarted.commits != 1 {
		t.Fatalf("full receipts blocked local recovery: restored=%v running=%v commits=%d error=%v", restored, restarted.running, restarted.commits, err)
	}
	after, err := store.Load()
	if err != nil || after.Applied == nil || after.Applied.Generation != 16 || len(after.PendingReceipts) != 2 || after.PendingReceipts[0].AttemptID == before.PendingReceipts[30].AttemptID {
		t.Fatalf("recovery did not supersede previous process receipts: %+v, %v", after, err)
	}
	if restored, err := r.EnsureApplied(context.Background()); err != nil || restored || restarted.commits != 1 {
		t.Fatalf("recovered live process was restarted: %v, %v", restored, err)
	}
}

type receiptRecoveryAdapter struct {
	*recordingLiveAdapter
	survivingComponent bool
}

func (a *receiptRecoveryAdapter) HasLiveProcess() bool {
	return a.running || a.survivingComponent
}

func TestLegacyPendingWithFullReceiptsRecoversOnlyAfterProcessExit(t *testing.T) {
	for _, scenario := range []string{"dead", "live", "surviving-component", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "state.json")
			store := state.NewAtomicStateStore(path, filepath.Join(dir, "configs"))
			file, err := engine.NewDryRunFileAdapter(filepath.Join(dir, "staged.json"), filepath.Join(dir, "active.json"))
			if err != nil {
				t.Fatal(err)
			}
			initial, err := New(store, &recordingLiveAdapter{DryRunFileAdapter: file}, nil, "test-credential")
			if err != nil {
				t.Fatal(err)
			}
			desired := testDesired(1, `{"schema_version":1,"fragments":[]}`)
			for gen := 1; gen <= 16; gen++ {
				desired.Generation = agentv1.NodeConfigGeneration(gen)
				if err := initial.Apply(context.Background(), desired); err != nil {
					t.Fatal(err)
				}
			}
			desired.Generation++
			// Older agents could journal a new pending apply before discovering
			// that the receipt queue was full. Model a crash at that boundary.
			if _, err := store.Begin(desired, time.Now()); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			adapter := &receiptRecoveryAdapter{
				recordingLiveAdapter: &recordingLiveAdapter{DryRunFileAdapter: file, running: scenario == "live"},
				survivingComponent:   scenario == "surviving-component",
			}
			reloaded, err := New(store, adapter, nil, "test-credential")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "canceled" {
				cancel()
			}
			err = reloaded.RecoverPending(ctx)
			if scenario != "dead" {
				want := state.ErrApplyReceiptQueueFull
				if scenario == "canceled" {
					want = context.Canceled
				}
				if !errors.Is(err, want) || adapter.commits != 0 {
					t.Fatalf("unsafe pending recovery: commits=%d error=%v", adapter.commits, err)
				}
				after, readErr := os.ReadFile(path)
				if readErr != nil || string(before) != string(after) {
					t.Fatalf("rejected recovery changed durable state: %v", readErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("previous-process receipts blocked pending recovery: %v", err)
			}
			after, err := store.Load()
			if err != nil || !adapter.running || adapter.commits != 1 || after.Pending != nil || after.Applied == nil || after.Applied.Generation != 17 || len(after.PendingReceipts) != 2 {
				t.Fatalf("pending apply was not recovered: state=%+v commits=%d error=%v", after, adapter.commits, err)
			}
		})
	}
}
