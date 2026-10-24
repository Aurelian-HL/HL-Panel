package runtime

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/engine"
	"github.com/hongle/hl-panel/internal/agent/trafficcontrol"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// The engine fixture models an exited owned process; TC uses a separate durable
// file and simulated kernel, as it does across an Agent or host restart.
type tcRecoveryAdapter struct {
	engine.EngineAdapter
	runner          *tcKernelRunner
	live            bool
	commits         int
	atCommit        map[string]string
	validationError error
	rollbacks       int
}

func (a *tcRecoveryAdapter) RequiresLiveProcess() bool { return true }
func (a *tcRecoveryAdapter) HasLiveProcess() bool      { return a.live }
func (a *tcRecoveryAdapter) ActiveEngineMode() string {
	if a.live {
		return "xray"
	}
	return ""
}
func (a *tcRecoveryAdapter) Commit(ctx context.Context, p engine.PreparedConfiguration) error {
	a.commits++
	a.atCommit = cloneTCFilters(a.runner.filters)
	if err := a.EngineAdapter.Commit(ctx, p); err != nil {
		return err
	}
	a.live = true
	return nil
}
func (a *tcRecoveryAdapter) Validate(ctx context.Context, p engine.PreparedConfiguration) error {
	if a.validationError != nil {
		return a.validationError
	}
	return a.EngineAdapter.Validate(ctx, p)
}
func (a *tcRecoveryAdapter) Rollback(ctx context.Context, p *engine.PreparedConfiguration) error {
	a.rollbacks++
	return a.EngineAdapter.Rollback(ctx, p)
}

func cloneTCFilters(filters map[string]string) map[string]string {
	copy := make(map[string]string, len(filters))
	for key, value := range filters {
		copy[key] = value
	}
	return copy
}

func TestBootstrapAlignsTrafficControlWithRecoveredGeneration(t *testing.T) {
	for _, pending := range []bool{false, true} {
		name := "TC persisted before engine transaction"
		if pending {
			name = "pending engine transaction with older TC state"
		}
		t.Run(name, func(t *testing.T) {
			a := newControlSafetyAgent(t, http.NotFound)
			runner := &tcKernelRunner{filters: map[string]string{}}
			tcPath := filepath.Join(a.cfg.DataDir, "tc.json")
			tc, err := trafficcontrol.New("eth0", "tc", tcPath, runner)
			if err != nil {
				t.Fatal(err)
			}
			a.trafficControl = tc
			previous := tcDesired(t, 1, map[int]int{12001: 10, 12002: 20})
			candidate := tcDesired(t, 2, map[int]int{12001: 50, 12003: 30})
			if err := a.applyWithUsage(context.Background(), previous); err != nil {
				t.Fatal(err)
			}
			wantGeneration := previous.Generation
			want := cloneTCFilters(runner.filters)
			if pending {
				// Model a journaled apply whose independent TC file is older.
				if _, err := a.state.Begin(candidate, time.Now()); err != nil {
					t.Fatal(err)
				}
				wantGeneration = candidate.Generation
				want = map[string]string{"ingress/22001": "50mbit", "egress/22001": "50mbit", "ingress/22003": "30mbit", "egress/22003": "30mbit"}
			} else if err := tc.ApplyBundle(context.Background(), candidate); err != nil {
				// Model a crash after TC persistence but before state.Begin.
				t.Fatal(err)
			}
			restarted, err := New(a.cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			restarted.trafficControl, err = trafficcontrol.New("eth0", "tc", tcPath, runner)
			if err != nil {
				t.Fatal(err)
			}
			adapter := &tcRecoveryAdapter{EngineAdapter: restarted.adapter, runner: runner}
			restarted.adapter = adapter
			if err := restarted.Bootstrap(context.Background()); err != nil {
				t.Fatal(err)
			}
			current, err := restarted.state.Load()
			if err != nil || current.Applied == nil || current.Applied.Generation != wantGeneration || current.Pending != nil {
				t.Fatalf("recovered engine state = %+v, %v", current, err)
			}
			if !reflect.DeepEqual(runner.filters, want) {
				t.Errorf("engine generation %d recovered with different TC generation: got=%v want=%v", wantGeneration, runner.filters, want)
			}
			if !reflect.DeepEqual(adapter.atCommit, want) {
				t.Errorf("engine activated before matching limits: got=%v want=%v", adapter.atCommit, want)
			}
			if adapter.commits != 1 {
				t.Fatalf("recovery commits = %d", adapter.commits)
			}
		})
	}
}

func tcRecoveryFixture(t *testing.T, withLKG bool) (*Agent, *tcKernelRunner, string, agentv1.DesiredNodeConfig) {
	t.Helper()
	a := newControlSafetyAgent(t, http.NotFound)
	runner := &tcKernelRunner{filters: map[string]string{}}
	tcPath := filepath.Join(a.cfg.DataDir, "tc.json")
	tc, err := trafficcontrol.New("eth0", "tc", tcPath, runner)
	if err != nil {
		t.Fatal(err)
	}
	a.trafficControl = tc
	desired := tcDesired(t, 1, map[int]int{12001: 10, 12002: 20})
	if withLKG {
		err = a.applyWithUsage(context.Background(), desired)
	} else {
		err = tc.ApplyBundle(context.Background(), desired)
	}
	if err != nil {
		t.Fatal(err)
	}
	return a, runner, tcPath, desired
}

func reloadTCRecoveryAgent(t *testing.T, initial *Agent, runner *tcKernelRunner, tcPath string, tcRunner trafficcontrol.Runner) (*Agent, *tcRecoveryAdapter) {
	t.Helper()
	a, err := New(initial.cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tcRunner == nil {
		tcRunner = runner
	}
	a.trafficControl, err = trafficcontrol.New("eth0", "tc", tcPath, tcRunner)
	if err != nil {
		t.Fatal(err)
	}
	adapter := &tcRecoveryAdapter{EngineAdapter: a.adapter, runner: runner}
	a.adapter = adapter
	return a, adapter
}

func TestTrafficControlRecoveryAcrossLifecycleEntries(t *testing.T) {
	for _, entry := range []string{"watchdog", "same-generation replay", "explicit restart", "host reboot"} {
		t.Run(entry, func(t *testing.T) {
			initial, runner, tcPath, desired := tcRecoveryFixture(t, true)
			a, adapter := reloadTCRecoveryAgent(t, initial, runner, tcPath, nil)
			if err := a.Bootstrap(context.Background()); err != nil {
				t.Fatal(err)
			}
			want := cloneTCFilters(runner.filters)
			candidate := tcDesired(t, 2, map[int]int{12001: 50, 12003: 30})
			if err := a.trafficControl.ApplyBundle(context.Background(), candidate); err != nil {
				t.Fatal(err)
			}
			adapter.live = false
			if entry == "host reboot" {
				runner.filters = map[string]string{}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			var err error
			switch entry {
			case "same-generation replay":
				err = a.applyWithUsage(ctx, desired)
			case "explicit restart":
				err = a.restartEngine(ctx)
			default:
				err = a.recoverEngine(ctx)
			}
			if err != nil {
				t.Fatal(err)
			}
			if adapter.commits != 2 || !reflect.DeepEqual(adapter.atCommit, want) || !reflect.DeepEqual(runner.filters, want) {
				t.Fatalf("recovery failed to align limits before start: commits=%d atCommit=%v filters=%v", adapter.commits, adapter.atCommit, runner.filters)
			}
			commands := runner.commands
			for i := 0; i < 5; i++ {
				if err := a.recoverEngine(ctx); err != nil {
					t.Fatal(err)
				}
				if err := a.applyWithUsage(ctx, desired); err != nil {
					t.Fatal(err)
				}
			}
			if adapter.commits != 2 || runner.commands != commands || adapter.rollbacks != 0 {
				t.Fatalf("healthy recovery tick or replay repeated mutations: commits=%d TC=%d rollbacks=%d", adapter.commits, runner.commands-commands, adapter.rollbacks)
			}
		})
	}
}

func TestBootstrapRetainsIndependentTrafficControlWithoutEngineLKG(t *testing.T) {
	initial, runner, tcPath, _ := tcRecoveryFixture(t, false)
	want := cloneTCFilters(runner.filters)
	// Kernel filters may have been cleared independently; the separate durable
	// limits remain necessary even when an orphaned process lacks its LKG.
	runner.filters = map[string]string{}
	a, adapter := reloadTCRecoveryAgent(t, initial, runner, tcPath, nil)
	adapter.live = true
	if err := a.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(runner.filters, want) || adapter.commits != 0 || adapter.rollbacks != 0 || !adapter.live {
		t.Fatalf("missing-LKG bootstrap disturbed live forwarding: limits=%v commits=%d rollbacks=%d", runner.filters, adapter.commits, adapter.rollbacks)
	}
}

func TestEmptyEngineLKGRemovesStaleTrafficControlWithoutStartingEngine(t *testing.T) {
	initial, runner, tcPath, _ := tcRecoveryFixture(t, false)
	desired := tcDesired(t, 1, nil)
	if err := initial.applyWithUsage(context.Background(), desired); err != nil {
		t.Fatal(err)
	}
	if err := initial.trafficControl.ApplyBundle(context.Background(), tcDesired(t, 2, map[int]int{12003: 30})); err != nil {
		t.Fatal(err)
	}
	a, adapter := reloadTCRecoveryAgent(t, initial, runner, tcPath, nil)
	if err := a.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.filters) != 0 || adapter.commits != 0 || adapter.live {
		t.Fatalf("empty LKG activated an engine or retained stale limits: limits=%v commits=%d", runner.filters, adapter.commits)
	}
}

type tcRecoveryFailureRunner struct {
	kernel     *tcKernelRunner
	failEgress bool
	failure    error
}

func (r *tcRecoveryFailureRunner) Run(ctx context.Context, binary string, args ...string) error {
	if r.failEgress && len(args) > 4 && args[0] == "filter" && args[1] == "replace" && args[4] == "egress" {
		r.failEgress = false
		return r.failure
	}
	return r.kernel.Run(ctx, binary, args...)
}

func TestRecoveryPrerequisiteFailureDoesNotTouchEngine(t *testing.T) {
	for _, failure := range []string{"validation", "partial TC", "TC persistence", "canceled"} {
		for _, pending := range []bool{false, true} {
			name := failure + "/applied"
			if pending {
				name = failure + "/pending"
			}
			t.Run(name, func(t *testing.T) {
				initial, runner, tcPath, _ := tcRecoveryFixture(t, true)
				candidate := tcDesired(t, 2, map[int]int{12001: 50, 12003: 30})
				if pending {
					if _, err := initial.state.Begin(candidate, time.Now()); err != nil {
						t.Fatal(err)
					}
				}
				wantFilters := cloneTCFilters(runner.filters)
				beforeTC, err := os.ReadFile(tcPath)
				if err != nil {
					t.Fatal(err)
				}
				beforeState, err := os.ReadFile(initial.cfg.StatePath())
				if err != nil {
					t.Fatal(err)
				}
				injected := errors.New("injected recovery prerequisite failure")
				tcRunner := &tcRecoveryFailureRunner{kernel: runner, failEgress: failure == "partial TC", failure: injected}
				a, adapter := reloadTCRecoveryAgent(t, initial, runner, tcPath, tcRunner)
				if failure == "validation" {
					adapter.validationError = injected
				}
				if failure == "TC persistence" {
					if err := os.Mkdir(tcPath+".tmp", 0700); err != nil {
						t.Fatal(err)
					}
				}
				commands := runner.commands
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if failure == "canceled" {
					cancel()
				}
				if err := a.Bootstrap(ctx); err == nil {
					t.Fatal("failed recovery was accepted")
				} else if failure == "canceled" && !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled recovery = %v", err)
				}
				if adapter.commits != 0 || adapter.rollbacks != 0 || !reflect.DeepEqual(runner.filters, wantFilters) {
					t.Fatalf("failed prerequisite changed forwarding: commits=%d rollbacks=%d limits=%v", adapter.commits, adapter.rollbacks, runner.filters)
				}
				afterTC, err := os.ReadFile(tcPath)
				if err != nil || string(beforeTC) != string(afterTC) {
					t.Fatalf("failed prerequisite changed durable TC: %v", err)
				}
				if failure == "canceled" || failure == "validation" {
					if runner.commands != commands {
						t.Fatalf("pre-validation failure issued %d TC commands", runner.commands-commands)
					}
				}
				if failure == "canceled" {
					afterState, err := os.ReadFile(initial.cfg.StatePath())
					if err != nil || string(beforeState) != string(afterState) {
						t.Fatalf("canceled recovery changed durable engine state: %v", err)
					}
				}
			})
		}
	}
}
