package reconciler

import (
	"context"
	"errors"
	"testing"

	"github.com/hongle/hl-panel/internal/agent/engine"
	"github.com/hongle/hl-panel/internal/agent/state"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// Model a mixed adapter whose aggregate health is empty while one component
// still serves connections. Pre-commit recovery must not roll it back to nil.
type partiallyLiveAdapter struct {
	*recordingLiveAdapter
	failPhase agentv1.ApplyPhase
	survivor  bool
	rollbacks int
}

func (a *partiallyLiveAdapter) Prepare(ctx context.Context, desired agentv1.DesiredNodeConfig) (engine.PreparedConfiguration, error) {
	if a.failPhase == agentv1.ApplyPhasePrepare {
		return engine.PreparedConfiguration{}, errors.New("temporary staging failure")
	}
	return a.recordingLiveAdapter.Prepare(ctx, desired)
}

func (a *partiallyLiveAdapter) Validate(ctx context.Context, prepared engine.PreparedConfiguration) error {
	if a.failPhase == agentv1.ApplyPhaseValidate {
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
