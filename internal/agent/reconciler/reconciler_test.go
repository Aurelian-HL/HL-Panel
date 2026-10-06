package reconciler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/engine"
	"github.com/hongle/hl-panel/internal/agent/state"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type recordingReporter struct {
	results        []agentv1.ApplyResultRequest
	failVerifyOnce bool
}

func (r *recordingReporter) ReportApplyResult(_ context.Context, _ string, result agentv1.ApplyResultRequest) error {
	if result.Phase == agentv1.ApplyPhaseVerify && r.failVerifyOnce {
		r.failVerifyOnce = false
		return errors.New("temporary report failure")
	}
	r.results = append(r.results, result)
	return nil
}

func TestVerifiedReceiptsSurviveReportFailureAndRetrySameGeneration(t *testing.T) {
	directory := t.TempDir()
	store := state.NewAtomicStateStore(directory+"/state.json", directory+"/configurations")
	fileAdapter, err := engine.NewDryRunFileAdapter(directory+"/staging/config.json", directory+"/active/config.json")
	if err != nil {
		t.Fatal(err)
	}
	adapter := &recordingLiveAdapter{DryRunFileAdapter: fileAdapter}
	reporter := &recordingReporter{failVerifyOnce: true}
	r, err := New(store, adapter, reporter, "node-secret")
	if err != nil {
		t.Fatal(err)
	}
	desired := testDesired(1, `{"schema_version":1,"fragments":[]}`)
	if err := r.Apply(context.Background(), desired); err != nil {
		t.Fatal(err)
	}
	current, err := store.Load()
	if err != nil || current.Applied == nil || len(current.PendingReceipts) != 1 ||
		current.PendingReceipts[0].Phase != agentv1.ApplyPhaseVerify {
		t.Fatalf("durable receipt after failed report = %#v; error = %v", current, err)
	}
	firstAttempt := current.PendingReceipts[0].AttemptID
	if adapter.commits != 1 {
		t.Fatalf("engine commits = %d, want 1", adapter.commits)
	}
	resumed, err := New(store, adapter, reporter, "node-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := resumed.FlushReceipts(context.Background()); err != nil {
		t.Fatalf("retry without desired configuration = %v", err)
	}
	if err := resumed.Apply(context.Background(), desired); err != nil {
		t.Fatal(err)
	}
	current, err = store.Load()
	if err != nil || len(current.PendingReceipts) != 0 || adapter.commits != 1 {
		t.Fatalf("retry state = %#v; commits = %d; error = %v", current, adapter.commits, err)
	}
	last := reporter.results[len(reporter.results)-1]
	if last.Phase != agentv1.ApplyPhaseVerify || last.AttemptID != firstAttempt || last.EngineMode != "gost" {
		t.Fatalf("retried receipt = %#v", last)
	}
}

func TestRestoreSupersedesOldQueuedAttempt(t *testing.T) {
	directory := t.TempDir()
	store := state.NewAtomicStateStore(directory+"/state.json", directory+"/configurations")
	fileAdapter, err := engine.NewDryRunFileAdapter(directory+"/staging/config.json", directory+"/active/config.json")
	if err != nil {
		t.Fatal(err)
	}
	adapter := &recordingLiveAdapter{DryRunFileAdapter: fileAdapter}
	reporter := &recordingReporter{failVerifyOnce: true}
	r, _ := New(store, adapter, reporter, "node-secret")
	desired := testDesired(1, `{"schema_version":1,"fragments":[]}`)
	if err := r.Apply(context.Background(), desired); err != nil {
		t.Fatal(err)
	}
	old, err := store.PendingApplyReceipts()
	if err != nil || len(old) != 1 {
		t.Fatalf("old receipt = %#v; error = %v", old, err)
	}
	adapter.running = false
	restarted, _ := New(store, adapter, reporter, "node-secret")
	if err := restarted.RestoreApplied(context.Background()); err != nil {
		t.Fatal(err)
	}
	if adapter.commits != 2 {
		t.Fatalf("restore commits = %d, want 2", adapter.commits)
	}
	current, err := store.Load()
	if err != nil || len(current.PendingReceipts) != 0 {
		t.Fatalf("restored receipt queue = %#v; error = %v", current, err)
	}
	newAttempt := reporter.results[len(reporter.results)-1].AttemptID
	if newAttempt == old[0].AttemptID {
		t.Fatal("restore reused previous apply attempt")
	}
}

func TestReconcilerReportsOrderedSuccessfulPhases(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	store := state.NewAtomicStateStore(directory+"/state.json", directory+"/configurations")
	adapter, err := engine.NewDryRunFileAdapter(directory+"/staging/config.json", directory+"/active/config.json")
	if err != nil {
		t.Fatalf("NewDryRunFileAdapter() error = %v", err)
	}
	reporter := &recordingReporter{}
	reconciler, err := New(store, adapter, reporter, "node-secret")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	desired := testDesired(1, `{"schema_version":1,"fragments":[]}`)
	if err := reconciler.Apply(context.Background(), desired); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	wantPhases := []agentv1.ApplyPhase{agentv1.ApplyPhasePrepare, agentv1.ApplyPhaseValidate, agentv1.ApplyPhaseCommit, agentv1.ApplyPhaseVerify}
	if len(reporter.results) != len(wantPhases) {
		t.Fatalf("reported %d phases, want %d (%#v)", len(reporter.results), len(wantPhases), reporter.results)
	}
	for index, phase := range wantPhases {
		if reporter.results[index].Phase != phase || reporter.results[index].Status != agentv1.ApplyStatusSucceeded {
			t.Fatalf("result[%d] = %#v, want phase %q succeeded", index, reporter.results[index], phase)
		}
	}
	current, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if current.Applied == nil || current.Applied.Generation != desired.Generation || current.Pending != nil {
		t.Fatalf("state after Apply() = %#v, want applied generation and no pending", current)
	}
}

func TestReconcilerValidationFailureRollsBackLastKnownGood(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	store := state.NewAtomicStateStore(directory+"/state.json", directory+"/configurations")
	adapter, err := engine.NewDryRunFileAdapter(directory+"/staging/config.json", directory+"/active/config.json")
	if err != nil {
		t.Fatalf("NewDryRunFileAdapter() error = %v", err)
	}
	reporter := &recordingReporter{}
	reconciler, err := New(store, adapter, reporter, "node-secret")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	first := testDesired(1, `{"schema_version":1,"fragments":[]}`)
	if err := reconciler.Apply(context.Background(), first); err != nil {
		t.Fatalf("Apply(first) error = %v", err)
	}
	invalid := testDesired(2, `{"schema_version":1,"fragments":[{"group_id":"bad","group_generation":1,"engine":"unknown","config":{}}]}`)
	if err := reconciler.Apply(context.Background(), invalid); !errors.Is(err, engine.ErrUnsupportedEngine) {
		t.Fatalf("Apply(invalid) error = %v, want ErrUnsupportedEngine", err)
	}
	lastKnownGood, ok, err := store.LastKnownGood()
	if err != nil || !ok || lastKnownGood.Generation != first.Generation {
		t.Fatalf("LastKnownGood() = (%#v, %v, %v), want first generation", lastKnownGood, ok, err)
	}
	current, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if current.Applied == nil || current.Applied.Generation != first.Generation || current.Pending != nil {
		t.Fatalf("state after rollback = %#v", current)
	}
	last := reporter.results[len(reporter.results)-1]
	if last.Phase != agentv1.ApplyPhaseRollback || last.Status != agentv1.ApplyStatusRolledBack {
		t.Fatalf("last report = %#v, want rolled back", last)
	}
}

func TestReconcilerRecoversPendingAfterRestart(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	store := state.NewAtomicStateStore(directory+"/state.json", directory+"/configurations")
	desired := testDesired(1, `{"schema_version":1,"fragments":[]}`)
	if _, err := store.Begin(desired, time.Now()); err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	adapter, err := engine.NewDryRunFileAdapter(directory+"/staging/config.json", directory+"/active/config.json")
	if err != nil {
		t.Fatalf("NewDryRunFileAdapter() error = %v", err)
	}
	reconciler, err := New(store, adapter, nil, "")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := reconciler.RecoverPending(context.Background()); err != nil {
		t.Fatalf("RecoverPending() error = %v", err)
	}
	current, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if current.Applied == nil || current.Applied.Generation != desired.Generation || current.Pending != nil {
		t.Fatalf("state after recovery = %#v", current)
	}
}

func TestReconcilerRollsBackWhenApplyContextIsCanceled(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	store := state.NewAtomicStateStore(directory+"/state.json", directory+"/configurations")
	baseAdapter, err := engine.NewDryRunFileAdapter(directory+"/staging/config.json", directory+"/active/config.json")
	if err != nil {
		t.Fatalf("NewDryRunFileAdapter() error = %v", err)
	}
	cancelingAdapter := &cancelingVerifyAdapter{DryRunFileAdapter: baseAdapter}
	reconciler, err := New(store, cancelingAdapter, nil, "")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	first := testDesired(1, `{"schema_version":1,"fragments":[]}`)
	if err := reconciler.Apply(context.Background(), first); err != nil {
		t.Fatalf("Apply(first) error = %v", err)
	}
	second := testDesired(2, `{"schema_version":1,"fragments":[{"group_id":"next","group_generation":1,"engine":"xray","config":{}}]}`)
	canceled, cancel := context.WithCancel(context.Background())
	cancelingAdapter.cancel = cancel
	cancelingAdapter.failVerify = true
	if err := reconciler.Apply(canceled, second); !errors.Is(err, context.Canceled) {
		t.Fatalf("Apply(canceled) error = %v, want context.Canceled", err)
	}
	lastKnownGood, ok, err := store.LastKnownGood()
	if err != nil || !ok || lastKnownGood.Generation != first.Generation {
		t.Fatalf("LastKnownGood() after canceled apply = (%#v, %v, %v)", lastKnownGood, ok, err)
	}
}

func TestRestoreAppliedAttestsRunningEngineAndRecoversStoppedProcess(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	store := state.NewAtomicStateStore(directory+"/state.json", directory+"/configurations")
	fileAdapter, err := engine.NewDryRunFileAdapter(directory+"/staging/config.json", directory+"/active/config.json")
	if err != nil {
		t.Fatalf("NewDryRunFileAdapter() error = %v", err)
	}
	desired := testDesired(1, `{"schema_version":1,"fragments":[]}`)
	first, err := New(store, fileAdapter, nil, "")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := first.Apply(context.Background(), desired); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	live := &recordingLiveAdapter{DryRunFileAdapter: fileAdapter}
	reporter := &recordingReporter{}
	restarted, err := New(store, live, reporter, "node-secret")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := restarted.RestoreApplied(context.Background()); err != nil {
		t.Fatalf("RestoreApplied() error = %v", err)
	}
	if live.commits != 1 || !live.running {
		t.Fatalf("restored adapter commits = %d, running = %v", live.commits, live.running)
	}
	if len(reporter.results) != 4 {
		t.Fatalf("restore reports = %#v, want prepare, validate, commit and verify", reporter.results)
	}
	attemptID := reporter.results[0].AttemptID
	if len(attemptID) != 32 {
		t.Fatalf("restore attempt id = %q, want 32 hex characters", attemptID)
	}
	for index, result := range reporter.results {
		if result.AttemptID != attemptID || result.Status != agentv1.ApplyStatusSucceeded {
			t.Fatalf("restored report = %#v, want same successful attempt", result)
		}
		if index >= 2 && result.EngineMode != "gost" {
			t.Fatalf("restored report = %#v, want real engine acknowledgement", result)
		}
	}
	live.running = false
	if err := restarted.Apply(context.Background(), desired); err != nil {
		t.Fatalf("Apply(stopped process) error = %v", err)
	}
	if live.commits != 2 || !live.running {
		t.Fatalf("recovered adapter commits = %d, running = %v", live.commits, live.running)
	}
	current, err := store.Load()
	if err != nil || current.Applied == nil || current.Applied.Generation != desired.Generation {
		t.Fatalf("Load() after restore = (%#v, %v)", current, err)
	}
}

func TestRestoreAppliedFailureDoesNotAttestEngine(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	store := state.NewAtomicStateStore(directory+"/state.json", directory+"/configurations")
	fileAdapter, err := engine.NewDryRunFileAdapter(directory+"/staging/config.json", directory+"/active/config.json")
	if err != nil {
		t.Fatalf("NewDryRunFileAdapter() error = %v", err)
	}
	desired := testDesired(1, `{"schema_version":1,"fragments":[]}`)
	first, _ := New(store, fileAdapter, nil, "")
	if err := first.Apply(context.Background(), desired); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	live := &recordingLiveAdapter{DryRunFileAdapter: fileAdapter, failCommit: true}
	reporter := &recordingReporter{}
	restarted, _ := New(store, live, reporter, "node-secret")
	if err := restarted.RestoreApplied(context.Background()); err == nil {
		t.Fatal("RestoreApplied() succeeded despite failed commit")
	}
	if len(reporter.results) != 3 || reporter.results[2].Status != agentv1.ApplyStatusFailed {
		t.Fatalf("failure reports = %#v, want failed commit after prepare and validate", reporter.results)
	}
	for _, result := range reporter.results {
		if result.EngineMode != "" {
			t.Fatalf("failure report falsely attests an engine: %#v", result)
		}
	}
	current, err := store.Load()
	if err != nil || current.LastApplyStatus != agentv1.ApplyStatusFailed {
		t.Fatalf("Load() after failed restore = (%#v, %v)", current, err)
	}
}

type recordingLiveAdapter struct {
	*engine.DryRunFileAdapter
	running    bool
	commits    int
	failCommit bool
}

func (a *recordingLiveAdapter) Commit(ctx context.Context, prepared engine.PreparedConfiguration) error {
	if a.failCommit {
		return errors.New("engine start failed")
	}
	if err := a.DryRunFileAdapter.Commit(ctx, prepared); err != nil {
		return err
	}
	a.commits++
	a.running = true
	return nil
}

func (a *recordingLiveAdapter) Rollback(ctx context.Context, previous *engine.PreparedConfiguration) error {
	a.running = false
	return a.DryRunFileAdapter.Rollback(ctx, previous)
}

func (a *recordingLiveAdapter) Verify(ctx context.Context, prepared engine.PreparedConfiguration) error {
	if !a.running {
		return errors.New("engine stopped")
	}
	return a.DryRunFileAdapter.Verify(ctx, prepared)
}

func (a *recordingLiveAdapter) RequiresLiveProcess() bool { return true }

func (a *recordingLiveAdapter) ActiveEngineMode() string {
	if a.running {
		return "gost"
	}
	return ""
}

type cancelingVerifyAdapter struct {
	*engine.DryRunFileAdapter
	cancel     context.CancelFunc
	failVerify bool
}

func (a *cancelingVerifyAdapter) Verify(ctx context.Context, prepared engine.PreparedConfiguration) error {
	if a.failVerify {
		a.cancel()
		return context.Canceled
	}
	return a.DryRunFileAdapter.Verify(ctx, prepared)
}

func testDesired(generation agentv1.NodeConfigGeneration, raw string) agentv1.DesiredNodeConfig {
	config := []byte(raw)
	sum := sha256.Sum256(config)
	return agentv1.DesiredNodeConfig{Generation: generation, Engine: agentv1.EngineNodeBundle, ConfigSHA256: hex.EncodeToString(sum[:]), Config: config}
}
