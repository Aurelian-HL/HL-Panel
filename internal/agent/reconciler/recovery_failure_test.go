package reconciler

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hongle/hl-panel/internal/agent/engine"
	"github.com/hongle/hl-panel/internal/agent/state"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// Model a mixed adapter whose aggregate health is empty while one component
// still serves connections. Pre-commit recovery must not roll it back to nil.
type partiallyLiveAdapter struct {
	*recordingLiveAdapter
	failPhase      agentv1.ApplyPhase
	failGeneration agentv1.NodeConfigGeneration
	survivor       bool
	rollbacks      int
}

type survivingRecoveryAdapter struct {
	*partiallyLiveAdapter
	failVerify bool
}

func (a *survivingRecoveryAdapter) HasLiveProcess() bool { return a.survivor }
func (a *survivingRecoveryAdapter) Verify(ctx context.Context, p engine.PreparedConfiguration) error {
	if a.failVerify {
		return errors.New("missing component failed verification")
	}
	return a.recordingLiveAdapter.Verify(ctx, p)
}

func TestPostCommitRecoveryFailurePreservesSurvivingProcess(t *testing.T) {
	for _, phase := range []agentv1.ApplyPhase{agentv1.ApplyPhaseCommit, agentv1.ApplyPhaseVerify} {
		for _, entry := range []string{"startup", "watchdog", "same-generation"} {
			t.Run(string(phase)+"/"+entry, func(t *testing.T) {
				dir := t.TempDir()
				store := state.NewAtomicStateStore(dir+"/state.json", dir+"/configurations")
				file, err := engine.NewDryRunFileAdapter(dir+"/staging/config.json", dir+"/active/config.json")
				if err != nil {
					t.Fatal(err)
				}
				desired := testDesired(1, `{"schema_version":1,"fragments":[{"group_id":"test","group_generation":1,"engine":"gost","config":{"services":[]}}]}`)
				initial, _ := New(store, file, nil, "")
				if err := initial.Apply(context.Background(), desired); err != nil {
					t.Fatal(err)
				}
				live := &partiallyLiveAdapter{recordingLiveAdapter: &recordingLiveAdapter{
					DryRunFileAdapter: file, failCommit: phase == agentv1.ApplyPhaseCommit,
				}, survivor: true}
				adapter := &survivingRecoveryAdapter{partiallyLiveAdapter: live, failVerify: phase == agentv1.ApplyPhaseVerify}
				reporter := &recordingReporter{}
				r, _ := New(store, adapter, reporter, "test-secret")
				var recoveryErr error
				switch entry {
				case "startup":
					recoveryErr = r.RestoreApplied(context.Background())
				case "watchdog":
					_, recoveryErr = r.EnsureApplied(context.Background())
				default:
					recoveryErr = r.Apply(context.Background(), desired)
				}
				if recoveryErr == nil || !live.survivor || live.rollbacks != 0 {
					t.Fatalf("recovery stopped surviving engine or falsely succeeded: err=%v survivor=%v rollbacks=%d", recoveryErr, live.survivor, live.rollbacks)
				}
				current, err := store.Load()
				if err != nil || current.Applied == nil || current.Applied.Generation != desired.Generation || current.LastApplyStatus != agentv1.ApplyStatusFailed || len(current.PendingReceipts) != 0 {
					t.Fatalf("failed recovery lost LKG or attested success: %#v, %v", current, err)
				}
				for _, result := range reporter.results {
					if result.Status == agentv1.ApplyStatusSucceeded {
						t.Fatal("failed recovery emitted successful receipt")
					}
				}
			})
		}
	}
}

type guardedLiveAdapter struct {
	*recordingLiveAdapter
	uncertain                      bool
	prepares, validates, rollbacks int
	rollbackErr                    error
}

func (a *guardedLiveAdapter) LiveUpdateError() error {
	if a.uncertain {
		return engine.ErrLiveStateUncertain
	}
	return nil
}
func (a *guardedLiveAdapter) Prepare(ctx context.Context, desired agentv1.DesiredNodeConfig) (engine.PreparedConfiguration, error) {
	a.prepares++
	return a.recordingLiveAdapter.Prepare(ctx, desired)
}
func (a *guardedLiveAdapter) Validate(ctx context.Context, p engine.PreparedConfiguration) error {
	a.validates++
	return a.recordingLiveAdapter.Validate(ctx, p)
}
func (a *guardedLiveAdapter) Rollback(ctx context.Context, p *engine.PreparedConfiguration) error {
	a.rollbacks++
	if a.rollbackErr != nil {
		return a.rollbackErr
	}
	return a.recordingLiveAdapter.Rollback(ctx, p)
}

func TestUncertainLiveStateRejectsRepeatedDeliveryBeforeStaging(t *testing.T) {
	dir := t.TempDir()
	store := state.NewAtomicStateStore(dir+"/state.json", dir+"/configurations")
	file, err := engine.NewDryRunFileAdapter(dir+"/staging/config.json", dir+"/active/config.json")
	if err != nil {
		t.Fatal(err)
	}
	desired := testDesired(1, `{"schema_version":1,"fragments":[]}`)
	initial, _ := New(store, file, nil, "")
	if err := initial.Apply(context.Background(), desired); err != nil {
		t.Fatal(err)
	}
	before, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	adapter := &guardedLiveAdapter{recordingLiveAdapter: &recordingLiveAdapter{DryRunFileAdapter: file}, uncertain: true}
	reporter := &recordingReporter{}
	r, _ := New(store, adapter, reporter, "test-secret")
	for _, generation := range []agentv1.NodeConfigGeneration{1, 2, 2, 3} {
		candidate := desired
		candidate.Generation = generation
		if err := r.Apply(context.Background(), candidate); !errors.Is(err, engine.ErrLiveStateUncertain) {
			t.Fatalf("uncertain apply falsely succeeded: %v", err)
		}
	}
	if err := r.RestoreApplied(context.Background()); !errors.Is(err, engine.ErrLiveStateUncertain) {
		t.Fatalf("uncertain restore falsely succeeded: %v", err)
	}
	after, err := store.Load()
	if err != nil || !reflect.DeepEqual(before, after) || adapter.prepares != 0 || adapter.validates != 0 || adapter.commits != 0 || adapter.rollbacks != 0 || len(reporter.results) != 0 {
		t.Fatalf("uncertain poll mutated state/runtime or receipts: adapter=%#v err=%v", adapter, err)
	}
}

func TestFailedApplyPreservesRollbackErrorIdentity(t *testing.T) {
	dir := t.TempDir()
	store := state.NewAtomicStateStore(dir+"/state.json", dir+"/configurations")
	file, err := engine.NewDryRunFileAdapter(dir+"/staging/config.json", dir+"/active/config.json")
	if err != nil {
		t.Fatal(err)
	}
	adapter := &guardedLiveAdapter{recordingLiveAdapter: &recordingLiveAdapter{DryRunFileAdapter: file, failCommit: true}, rollbackErr: engine.ErrLiveStateUncertain}
	r, _ := New(store, adapter, nil, "")
	err = r.Apply(context.Background(), testDesired(1, `{"schema_version":1,"fragments":[]}`))
	if !errors.Is(err, engine.ErrLiveStateUncertain) || adapter.rollbacks != 1 {
		t.Fatalf("rollback uncertainty was lost: %v", err)
	}
}

func (a *partiallyLiveAdapter) Prepare(ctx context.Context, desired agentv1.DesiredNodeConfig) (engine.PreparedConfiguration, error) {
	if a.failPhase == agentv1.ApplyPhasePrepare && (a.failGeneration == 0 || desired.Generation == a.failGeneration) {
		return engine.PreparedConfiguration{}, errors.New("temporary staging failure")
	}
	return a.recordingLiveAdapter.Prepare(ctx, desired)
}

func (a *partiallyLiveAdapter) Validate(ctx context.Context, prepared engine.PreparedConfiguration) error {
	if a.failPhase == agentv1.ApplyPhaseValidate && (a.failGeneration == 0 || prepared.Generation == a.failGeneration) {
		return errors.New("temporary validation failure")
	}
	return a.recordingLiveAdapter.Validate(ctx, prepared)
}

func (a *partiallyLiveAdapter) Rollback(ctx context.Context, previous *engine.PreparedConfiguration) error {
	a.rollbacks++
	if previous == nil {
		a.survivor = false
	}
	return a.recordingLiveAdapter.Rollback(ctx, previous)
}

func TestPreCommitApplyFailureDoesNotTouchSurvivingProcess(t *testing.T) {
	for _, phase := range []agentv1.ApplyPhase{agentv1.ApplyPhasePrepare, agentv1.ApplyPhaseValidate} {
		for _, hasPrevious := range []bool{false, true} {
			name := string(phase) + "/without-LKG"
			if hasPrevious {
				name = string(phase) + "/with-LKG"
			}
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				store := state.NewAtomicStateStore(dir+"/state.json", dir+"/configurations")
				file, err := engine.NewDryRunFileAdapter(dir+"/staging/config.json", dir+"/active/config.json")
				if err != nil {
					t.Fatal(err)
				}
				config := `{"schema_version":1,"fragments":[]}`
				if hasPrevious {
					initial, _ := New(store, file, nil, "")
					if err := initial.Apply(context.Background(), testDesired(1, config)); err != nil {
						t.Fatal(err)
					}
				}
				adapter := &partiallyLiveAdapter{
					recordingLiveAdapter: &recordingLiveAdapter{DryRunFileAdapter: file, running: true},
					failPhase:            phase, failGeneration: 2, survivor: true,
				}
				reporter := &recordingReporter{}
				r, _ := New(store, adapter, reporter, "test-secret")
				if err := r.Apply(context.Background(), testDesired(2, config)); err == nil {
					t.Fatal("failed staging or validation falsely succeeded")
				}
				if adapter.rollbacks != 0 || adapter.commits != 0 || !adapter.running || !adapter.survivor {
					t.Fatalf("pre-commit failure touched live service: %#v", adapter)
				}
				current, err := store.Load()
				if err != nil || current.Pending != nil || current.LastApplyPhase != phase || current.LastApplyStatus != agentv1.ApplyStatusFailed {
					t.Fatalf("failed staging state = %#v, %v", current, err)
				}
				if hasPrevious && (current.Applied == nil || current.Applied.Generation != 1) || !hasPrevious && current.Applied != nil {
					t.Fatal("pre-commit failure changed the applied marker")
				}
				for _, receipt := range reporter.results {
					if receipt.Phase == agentv1.ApplyPhaseRollback || receipt.Status == agentv1.ApplyStatusRolledBack {
						t.Fatal("staging failure falsely reported rollback")
					}
				}
			})
		}
	}
}

func TestPreCommitRecoveryFailurePreservesHealthyComponent(t *testing.T) {
	for _, phase := range []agentv1.ApplyPhase{agentv1.ApplyPhasePrepare, agentv1.ApplyPhaseValidate} {
		for _, entry := range []string{"startup", "watchdog", "same-generation"} {
			t.Run(string(phase)+"/"+entry, func(t *testing.T) {
				dir := t.TempDir()
				store := state.NewAtomicStateStore(dir+"/state.json", dir+"/configurations")
				file, err := engine.NewDryRunFileAdapter(dir+"/staging/config.json", dir+"/active/config.json")
				if err != nil {
					t.Fatal(err)
				}
				desired := testDesired(1, `{"schema_version":1,"fragments":[{"group_id":"test","group_generation":1,"engine":"gost","config":{"services":[]}}]}`)
				r, err := New(store, file, nil, "")
				if err != nil {
					t.Fatal(err)
				}
				if err := r.Apply(context.Background(), desired); err != nil {
					t.Fatal(err)
				}
				adapter := &partiallyLiveAdapter{
					recordingLiveAdapter: &recordingLiveAdapter{DryRunFileAdapter: file},
					failPhase:            phase, survivor: true,
				}
				reporter := &recordingReporter{}
				r, err = New(store, adapter, reporter, "test-secret")
				if err != nil {
					t.Fatal(err)
				}
				recover := func() error {
					switch entry {
					case "startup":
						return r.RestoreApplied(context.Background())
					case "watchdog":
						_, err := r.EnsureApplied(context.Background())
						return err
					default:
						return r.Apply(context.Background(), desired)
					}
				}
				if err := recover(); err == nil {
					t.Fatal("recovery falsely succeeded")
				}
				if !adapter.survivor || adapter.rollbacks != 0 || adapter.commits != 0 {
					t.Fatalf("pre-commit failure interrupted healthy component: survivor=%v rollbacks=%d commits=%d", adapter.survivor, adapter.rollbacks, adapter.commits)
				}
				current, err := store.Load()
				if err != nil || current.Applied == nil || current.Applied.Generation != desired.Generation || current.Pending != nil || current.LastApplyPhase != phase || current.LastApplyStatus != agentv1.ApplyStatusFailed || len(current.PendingReceipts) != 0 {
					t.Fatalf("failed recovery lost last-known-good or attested success: %#v, %v", current, err)
				}
				for _, result := range reporter.results {
					if result.Status == agentv1.ApplyStatusSucceeded || result.EngineMode != "" {
						t.Fatal("failed recovery attested live engine")
					}
				}
				adapter.failPhase = ""
				if err := recover(); err != nil || !adapter.running || !adapter.survivor {
					t.Fatalf("subsequent recovery did not restore missing component: %v", err)
				}
			})
		}
	}
}

type verifyFailingRecoveryAdapter struct {
	*partiallyLiveAdapter
}

func (a *verifyFailingRecoveryAdapter) Verify(context.Context, engine.PreparedConfiguration) error {
	return errors.New("activated engine failed verification")
}

// Once activation has been attempted, retain the existing safety cleanup.
// Skipping rollback for every failure would leave an unverified process live.
func TestPostCommitRecoveryFailureStillCleansUp(t *testing.T) {
	for _, phase := range []agentv1.ApplyPhase{agentv1.ApplyPhaseCommit, agentv1.ApplyPhaseVerify} {
		t.Run(string(phase), func(t *testing.T) {
			dir := t.TempDir()
			store := state.NewAtomicStateStore(dir+"/state.json", dir+"/configurations")
			file, err := engine.NewDryRunFileAdapter(dir+"/staging/config.json", dir+"/active/config.json")
			if err != nil {
				t.Fatal(err)
			}
			desired := testDesired(1, `{"schema_version":1,"fragments":[]}`)
			initial, _ := New(store, file, nil, "")
			if err := initial.Apply(context.Background(), desired); err != nil {
				t.Fatal(err)
			}
			live := &partiallyLiveAdapter{recordingLiveAdapter: &recordingLiveAdapter{
				DryRunFileAdapter: file, failCommit: phase == agentv1.ApplyPhaseCommit,
			}, survivor: true}
			var adapter engine.EngineAdapter = live
			if phase == agentv1.ApplyPhaseVerify {
				adapter = &verifyFailingRecoveryAdapter{partiallyLiveAdapter: live}
			}
			r, err := New(store, adapter, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := r.RestoreApplied(context.Background()); err == nil {
				t.Fatal("failed activation falsely succeeded")
			}
			if live.rollbacks != 1 || live.running || live.survivor {
				t.Fatalf("unverified activation was not cleaned up: %#v", live)
			}
			current, err := store.Load()
			if err != nil || current.Applied == nil || current.Applied.Generation != desired.Generation || current.LastApplyPhase != phase || current.LastApplyStatus != agentv1.ApplyStatusFailed || len(current.PendingReceipts) != 0 {
				t.Fatalf("cleanup lost last-known-good or attested success: %#v, %v", current, err)
			}
		})
	}
}

// A lost journal is not evidence that the owned data plane is empty. Refuse
// activation before a failure can reach Rollback(nil) and stop that survivor.
func TestApplyWithoutRollbackRecordPreservesLiveProcess(t *testing.T) {
	for _, running := range []bool{false, true} {
		t.Run(map[bool]string{false: "partial-mixed", true: "fully-live"}[running], func(t *testing.T) {
			dir := t.TempDir()
			store := state.NewAtomicStateStore(dir+"/state.json", dir+"/configurations")
			file, err := engine.NewDryRunFileAdapter(dir+"/staging/config.json", dir+"/active/config.json")
			if err != nil {
				t.Fatal(err)
			}
			live := &partiallyLiveAdapter{recordingLiveAdapter: &recordingLiveAdapter{
				DryRunFileAdapter: file, running: running,
			}, survivor: true}
			adapter := &survivingRecoveryAdapter{partiallyLiveAdapter: live, failVerify: true}
			reporter := &recordingReporter{}
			r, err := New(store, adapter, reporter, "test-secret")
			if err != nil {
				t.Fatal(err)
			}
			desired := testDesired(2, `{"schema_version":1,"fragments":[]}`)
			for i := 0; i < 2; i++ {
				if err := r.Apply(context.Background(), desired); !errors.Is(err, engine.ErrLiveUpdateRequiresMaintenance) {
					t.Fatalf("missing rollback record was not rejected before activation: %v", err)
				}
			}
			if !live.survivor || live.running != running || live.rollbacks != 0 || live.commits != 0 {
				t.Fatalf("lost journal interrupted owned process: %#v", live)
			}
			current, err := store.Load()
			if err != nil || current.Pending != nil || current.Applied != nil || current.LastApplyStatus != agentv1.ApplyStatusFailed {
				t.Fatalf("refusal attested an unapplied generation: %#v, %v", current, err)
			}
			for _, result := range reporter.results {
				if result.Phase == agentv1.ApplyPhaseRollback ||
					(result.Phase == agentv1.ApplyPhaseCommit || result.Phase == agentv1.ApplyPhaseVerify) && result.Status == agentv1.ApplyStatusSucceeded {
					t.Fatal("refused activation reported rollback or success")
				}
			}
		})
	}
}

func TestFirstApplyWithoutLiveProcessStillCleansFailedActivation(t *testing.T) {
	dir := t.TempDir()
	store := state.NewAtomicStateStore(dir+"/state.json", dir+"/configurations")
	file, err := engine.NewDryRunFileAdapter(dir+"/staging/config.json", dir+"/active/config.json")
	if err != nil {
		t.Fatal(err)
	}
	live := &partiallyLiveAdapter{recordingLiveAdapter: &recordingLiveAdapter{DryRunFileAdapter: file}}
	adapter := &survivingRecoveryAdapter{partiallyLiveAdapter: live, failVerify: true}
	r, err := New(store, adapter, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Apply(context.Background(), testDesired(1, `{"schema_version":1,"fragments":[]}`)); err == nil || errors.Is(err, engine.ErrLiveUpdateRequiresMaintenance) {
		t.Fatalf("first activation did not exercise verification failure: %v", err)
	}
	if live.commits != 1 || live.rollbacks != 1 || live.running {
		t.Fatalf("unverified first activation not cleaned up: %#v", live)
	}
}
