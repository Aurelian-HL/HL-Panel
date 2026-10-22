package reconciler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/engine"
	"github.com/hongle/hl-panel/internal/agent/state"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type stalledBatchReporter struct {
	started chan struct{}
	release chan struct{}
}

func (r *stalledBatchReporter) ReportApplyResult(context.Context, string, agentv1.ApplyResultRequest) error {
	return nil
}

func (r *stalledBatchReporter) ReportApplyResults(ctx context.Context, _ string, _ []agentv1.ApplyResultRequest) error {
	close(r.started)
	select {
	case <-r.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestBatchNetworkWaitDoesNotHoldEngineLifecycleLock(t *testing.T) {
	dir := t.TempDir()
	store := state.NewAtomicStateStore(dir+"/state.json", dir+"/configurations")
	adapter, err := engine.NewDryRunFileAdapter(dir+"/staging/config.json", dir+"/active/config.json")
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(store, adapter, &batchRecordingReporter{fail: true}, "node-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Apply(context.Background(), testDesired(1, `{"schema_version":1,"fragments":[]}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.ReceiptReady():
	default:
		t.Fatal("failed synchronous delivery did not signal retry")
	}
	reporter := &stalledBatchReporter{started: make(chan struct{}), release: make(chan struct{})}
	r.reporter = reporter
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer close(reporter.release)
	done := make(chan error, 1)
	go func() { done <- r.FlushReceipts(ctx) }()
	select {
	case <-reporter.started:
	case <-ctx.Done():
		t.Fatal("batch delivery did not start")
	}
	local := make(chan error, 1)
	go func() {
		if _, err := r.EnsureApplied(ctx); err != nil {
			local <- err
			return
		}
		local <- r.Apply(ctx, testDesired(2, `{"schema_version":1,"fragments":[]}`))
	}()
	select {
	case err := <-local:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("background receipt delivery blocked recovery or the next Apply return")
	}
	select {
	case <-r.ReceiptReady():
	default:
		t.Fatal("busy background delivery lost the next generation wake-up")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("unexpected flush cancellation", err)
	}
	if queue, err := store.PendingApplyReceipts(); err != nil || len(queue) != 4 || queue[2].Generation != 2 {
		t.Fatal("canceled delivery lost receipts", err)
	}
	current, err := store.Load()
	if err != nil || current.Applied == nil || current.Applied.Generation != 2 {
		t.Fatal("next generation did not persist while background delivery was blocked", err)
	}
}

func TestReceiptDeliveryHintsAreCoalescedAndAbsentAfterSynchronousSuccess(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "success"
		if fail {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			store := state.NewAtomicStateStore(dir+"/state.json", dir+"/configurations")
			adapter, err := engine.NewDryRunFileAdapter(dir+"/staging/config.json", dir+"/active/config.json")
			if err != nil {
				t.Fatal(err)
			}
			r, err := New(store, adapter, &batchRecordingReporter{fail: fail}, "node-secret")
			if err != nil {
				t.Fatal(err)
			}
			for generation := 1; generation <= 3; generation++ {
				if err := r.Apply(context.Background(), testDesired(agentv1.NodeConfigGeneration(generation), `{"schema_version":1,"fragments":[]}`)); err != nil {
					t.Fatal(err)
				}
			}
			wantHints, wantReceipts := 0, 0
			if fail {
				wantHints, wantReceipts = 1, 6
			}
			if got := len(r.receiptReady); got != wantHints {
				t.Fatalf("wake-up hints=%d, want %d", got, wantHints)
			}
			if queue, err := store.PendingApplyReceipts(); err != nil || len(queue) != wantReceipts {
				t.Fatalf("durable receipts=%d, want %d, error=%v", len(queue), wantReceipts, err)
			}
		})
	}
}

type deadlineBatchReporter struct {
	recordingReporter
	deadlines []time.Time
}

func (r *deadlineBatchReporter) ReportApplyResults(ctx context.Context, _ string, _ []agentv1.ApplyResultRequest) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return errors.New("immediate delivery must have a deadline")
	}
	r.deadlines = append(r.deadlines, deadline)
	return nil
}

func TestImmediateReceiptBacklogSharesOneDeadline(t *testing.T) {
	dir := t.TempDir()
	store := state.NewAtomicStateStore(dir+"/state.json", dir+"/configurations")
	adapter, err := engine.NewDryRunFileAdapter(dir+"/staging/config.json", dir+"/active/config.json")
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(store, adapter, &batchRecordingReporter{fail: true}, "node-secret")
	if err != nil {
		t.Fatal(err)
	}
	for generation := 1; generation <= 3; generation++ {
		if err := r.Apply(context.Background(), testDesired(agentv1.NodeConfigGeneration(generation), `{"schema_version":1,"fragments":[]}`)); err != nil {
			t.Fatal(err)
		}
	}
	reporter := &deadlineBatchReporter{}
	r.reporter = reporter
	started := time.Now()
	r.deliverReceipts(context.Background())
	if len(reporter.deadlines) != 3 {
		t.Fatal("did not deliver all queued generations", len(reporter.deadlines))
	}
	for _, deadline := range reporter.deadlines {
		if !deadline.Equal(reporter.deadlines[0]) || deadline.After(started.Add(applyReportTimeout+100*time.Millisecond)) {
			t.Fatal("receipt backlog renewed its synchronous delivery budget")
		}
	}
	if queue, err := store.PendingApplyReceipts(); err != nil || len(queue) != 0 {
		t.Fatal("successful backlog delivery was not acknowledged", err)
	}
}

type batchRecordingReporter struct {
	recordingReporter
	batches [][]agentv1.ApplyResultRequest
	fail    bool
}

func (r *batchRecordingReporter) ReportApplyResults(_ context.Context, _ string, pair []agentv1.ApplyResultRequest) error {
	if r.fail {
		return errors.New("temporary batch failure")
	}
	r.batches = append(r.batches, append([]agentv1.ApplyResultRequest(nil), pair...))
	return nil
}

func TestBatchReceiptsSurviveRestartAndPreserveGenerationOrder(t *testing.T) {
	dir := t.TempDir()
	store := state.NewAtomicStateStore(dir+"/state.json", dir+"/configurations")
	fileAdapter, err := engine.NewDryRunFileAdapter(dir+"/staging/config.json", dir+"/active/config.json")
	if err != nil {
		t.Fatal(err)
	}
	adapter := &recordingLiveAdapter{DryRunFileAdapter: fileAdapter}
	reporter := &batchRecordingReporter{fail: true}
	r, err := New(store, adapter, reporter, "node-secret")
	if err != nil {
		t.Fatal(err)
	}
	for generation := 1; generation <= 2; generation++ {
		if err := r.Apply(context.Background(), testDesired(agentv1.NodeConfigGeneration(generation), `{"schema_version":1,"fragments":[]}`)); err != nil {
			t.Fatal(err)
		}
	}
	queue, err := store.PendingApplyReceipts()
	if err != nil || len(queue) != 4 {
		t.Fatal("failed batch lost durable receipts", err)
	}
	if adapter.commits != 2 {
		t.Fatal("unexpected engine commit count", adapter.commits)
	}
	// Re-open the on-disk state to model agent restart; receipt retry must not
	// re-apply either configuration or merge separate generations together.
	reopened := state.NewAtomicStateStore(dir+"/state.json", dir+"/configurations")
	reporter.fail = false
	r, err = New(reopened, adapter, reporter, "node-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.FlushReceipts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(reporter.batches) != 2 || reporter.batches[0][0].Generation != 1 || reporter.batches[1][0].Generation != 2 {
		t.Fatal("generations merged or reordered")
	}
	for _, pair := range reporter.batches {
		if !agentv1.IsSuccessfulApplyPair(pair) {
			t.Fatal("invalid batch")
		}
	}
	if pending, err := reopened.PendingApplyReceipts(); err != nil || len(pending) != 0 || adapter.commits != 2 {
		t.Fatal("retry lost state or reactivated engine", err)
	}
	for _, result := range reporter.results {
		if result.Phase == agentv1.ApplyPhaseCommit || result.Phase == agentv1.ApplyPhaseVerify {
			t.Fatal("batch duplicated through single endpoint")
		}
	}
}

func TestSinglePendingVerifyUsesLegacyReporterEvenWithBatchSupport(t *testing.T) {
	dir := t.TempDir()
	store := state.NewAtomicStateStore(dir+"/state.json", dir+"/configurations")
	adapter, err := engine.NewDryRunFileAdapter(dir+"/staging/config.json", dir+"/active/config.json")
	if err != nil {
		t.Fatal(err)
	}
	reporter := &batchRecordingReporter{fail: true}
	r, _ := New(store, adapter, reporter, "node-secret")
	if err := r.Apply(context.Background(), testDesired(1, `{"schema_version":1,"fragments":[]}`)); err != nil {
		t.Fatal(err)
	}
	queue, err := store.PendingApplyReceipts()
	if err != nil || len(queue) != 2 {
		t.Fatal(err)
	}
	if err := store.AcknowledgeApplyReceipt(queue[0]); err != nil {
		t.Fatal(err)
	}
	if err := r.FlushReceipts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(reporter.batches) != 0 || reporter.results[len(reporter.results)-1].Phase != agentv1.ApplyPhaseVerify {
		t.Fatal("single receipt incorrectly batched")
	}
}
