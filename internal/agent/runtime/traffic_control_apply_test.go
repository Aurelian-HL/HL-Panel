package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/hongle/hl-panel/internal/agent/engine"
	"github.com/hongle/hl-panel/internal/agent/reconciler"
	"github.com/hongle/hl-panel/internal/agent/state"
	"github.com/hongle/hl-panel/internal/agent/trafficcontrol"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type tcFailureAdapter struct {
	engine.EngineAdapter
	failPhase          string
	failure            error
	cancel             context.CancelFunc
	live               bool
	commits, rollbacks int
}

func (a *tcFailureAdapter) fail(phase string, p engine.PreparedConfiguration) error {
	if p.Generation == 2 && a.failPhase == phase {
		if a.cancel != nil {
			a.cancel()
		}
		return a.failure
	}
	return nil
}
func (a *tcFailureAdapter) Validate(ctx context.Context, p engine.PreparedConfiguration) error {
	if err := a.fail("validate", p); err != nil {
		return err
	}
	return a.EngineAdapter.Validate(ctx, p)
}
func (a *tcFailureAdapter) Commit(ctx context.Context, p engine.PreparedConfiguration) error {
	a.commits++
	if err := a.fail("commit", p); err != nil {
		return err
	}
	return a.EngineAdapter.Commit(ctx, p)
}
func (a *tcFailureAdapter) Verify(ctx context.Context, p engine.PreparedConfiguration) error {
	if err := a.fail("verify", p); err != nil {
		return err
	}
	return a.EngineAdapter.Verify(ctx, p)
}
func (a *tcFailureAdapter) Rollback(ctx context.Context, p *engine.PreparedConfiguration) error {
	a.rollbacks++
	return a.EngineAdapter.Rollback(ctx, p)
}
func (a *tcFailureAdapter) HasLiveProcess() bool { return a.live }

type tcKernelRunner struct {
	filters  map[string]string
	commands int
}

func (r *tcKernelRunner) Run(ctx context.Context, _ string, args ...string) error {
	r.commands++
	if err := ctx.Err(); err != nil {
		return err
	}
	if args[0] != "filter" {
		return nil
	}
	key := args[4] + "/" + args[6]
	if args[1] == "del" {
		delete(r.filters, key)
		return nil
	}
	for i, arg := range args {
		if arg == "rate" {
			r.filters[key] = args[i+1]
		}
	}
	return nil
}

func tcDesired(t *testing.T, generation agentv1.NodeConfigGeneration, limits map[int]int) agentv1.DesiredNodeConfig {
	t.Helper()
	var fragments []agentv1.ConfigurationFragment
	for port, rate := range limits {
		fragments = append(fragments, agentv1.ConfigurationFragment{GroupID: "rule-" + strconv.Itoa(port), GroupRevision: 1, Engine: agentv1.EngineXray, Config: json.RawMessage(`{}`), Usage: &agentv1.UsageMetadata{RuleID: "rule-" + strconv.Itoa(port), Protocol: "tcp", ListenPort: port, SpeedLimitMbps: rate}})
	}
	raw, err := json.Marshal(agentv1.ConfigurationBundle{SchemaVersion: 1, Fragments: fragments})
	if err != nil {
		t.Fatal(err)
	}
	return agentv1.DesiredNodeConfig{Generation: generation, Engine: agentv1.EngineNodeBundle, Config: raw, ConfigSHA256: generations.SHA256Hex(raw)}
}

func TestEngineFailureRestoresExactTrafficControlWithoutRequestContext(t *testing.T) {
	for _, phase := range []string{"validate", "commit", "verify", "missing_lkg"} {
		t.Run(phase, func(t *testing.T) {
			dir := t.TempDir()
			store := state.NewAtomicStateStore(filepath.Join(dir, "state.json"), filepath.Join(dir, "configs"))
			activePath := filepath.Join(dir, "active.json")
			file, err := engine.NewDryRunFileAdapter(filepath.Join(dir, "staged.json"), activePath)
			if err != nil {
				t.Fatal(err)
			}
			adapter := &tcFailureAdapter{EngineAdapter: file}
			r, err := reconciler.New(store, adapter, nil, "test-credential")
			if err != nil {
				t.Fatal(err)
			}
			runner := &tcKernelRunner{filters: map[string]string{}}
			tcPath := filepath.Join(dir, "tc.json")
			tc, err := trafficcontrol.New("eth0", "tc", tcPath, runner)
			if err != nil {
				t.Fatal(err)
			}
			a := &Agent{state: store, reconciler: r, trafficControl: tc}
			previous := tcDesired(t, 1, map[int]int{12001: 10, 12002: 20})
			if phase == "missing_lkg" {
				// A running process can outlive its engine LKG. Do not substitute
				// an empty bundle and remove its still-valid limits on rejection.
				if err := tc.ApplyBundle(context.Background(), previous); err != nil {
					t.Fatal(err)
				}
				adapter.live = true
			} else if err := a.applyWithUsage(context.Background(), previous); err != nil {
				t.Fatal(err)
			}
			beforeTC, _ := os.ReadFile(tcPath)
			beforeEngine, _ := os.ReadFile(activePath)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			adapter.failPhase, adapter.failure, adapter.cancel = phase, errors.New("injected engine failure"), cancel
			candidate := tcDesired(t, 2, map[int]int{12001: 50, 12003: 30})
			got := a.applyWithUsage(ctx, candidate)
			if phase == "missing_lkg" {
				if !errors.Is(got, engine.ErrLiveUpdateRequiresMaintenance) || adapter.commits != 0 || adapter.rollbacks != 0 {
					t.Fatalf("unsafe missing-LKG handling: commits=%d rollbacks=%d error=%v", adapter.commits, adapter.rollbacks, got)
				}
			} else if !errors.Is(got, adapter.failure) {
				t.Fatalf("engine failure = %v", got)
			}
			afterTC, _ := os.ReadFile(tcPath)
			afterEngine, _ := os.ReadFile(activePath)
			if !reflect.DeepEqual(beforeTC, afterTC) || !reflect.DeepEqual(beforeEngine, afterEngine) {
				t.Fatalf("failed engine apply changed durable state: limits=%s engine=%s", afterTC, afterEngine)
			}
			want := map[string]string{"ingress/22001": "10mbit", "egress/22001": "10mbit", "ingress/22002": "20mbit", "egress/22002": "20mbit"}
			if !reflect.DeepEqual(runner.filters, want) {
				t.Fatalf("engine failure left kernel limits=%v", runner.filters)
			}
			current, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if current.Pending != nil || (current.Applied != nil && current.Applied.Generation != 1) {
				t.Fatalf("failed generation was accepted: %#v", current)
			}
		})
	}
}

func TestRepeatedGenerationWithAlteredBodyCannotChangeTrafficControl(t *testing.T) {
	dir := t.TempDir()
	store := state.NewAtomicStateStore(filepath.Join(dir, "state.json"), filepath.Join(dir, "configs"))
	activePath := filepath.Join(dir, "active.json")
	adapter, err := engine.NewDryRunFileAdapter(filepath.Join(dir, "staged.json"), activePath)
	if err != nil {
		t.Fatal(err)
	}
	r, err := reconciler.New(store, adapter, nil, "test-credential")
	if err != nil {
		t.Fatal(err)
	}
	runner := &tcKernelRunner{filters: map[string]string{}}
	tcPath := filepath.Join(dir, "tc.json")
	tc, err := trafficcontrol.New("eth0", "tc", tcPath, runner)
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{state: store, reconciler: r, trafficControl: tc}
	previous := tcDesired(t, 2, map[int]int{12001: 10, 12002: 20})
	if err := a.applyWithUsage(context.Background(), previous); err != nil {
		t.Fatal(err)
	}
	beforeTC, err := os.ReadFile(tcPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeEngine, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatal(err)
	}
	altered := tcDesired(t, previous.Generation, map[int]int{12001: 1})
	altered.ConfigSHA256 = previous.ConfigSHA256
	for _, test := range []struct {
		name    string
		desired agentv1.DesiredNodeConfig
		wantErr error
	}{
		{"altered replay", altered, state.ErrConfigurationHash},
		{"older generation", tcDesired(t, 1, map[int]int{12001: 1}), state.ErrGenerationRollback},
		{"conflicting generation", tcDesired(t, 2, map[int]int{12001: 1}), state.ErrGenerationConflict},
		{"identical replay", previous, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			commands := runner.commands
			if err := a.applyWithUsage(context.Background(), test.desired); !errors.Is(err, test.wantErr) {
				t.Fatalf("replay error = %v, want %v", err, test.wantErr)
			}
			if runner.commands != commands {
				t.Fatalf("rejected or identical replay issued %d TC commands", runner.commands-commands)
			}
		})
	}
	afterTC, _ := os.ReadFile(tcPath)
	afterEngine, _ := os.ReadFile(activePath)
	if !reflect.DeepEqual(beforeTC, afterTC) || !reflect.DeepEqual(beforeEngine, afterEngine) {
		t.Fatalf("altered replay changed durable state: limits=%s engine=%s", afterTC, afterEngine)
	}
	want := map[string]string{"ingress/22001": "10mbit", "egress/22001": "10mbit", "ingress/22002": "20mbit", "egress/22002": "20mbit"}
	if !reflect.DeepEqual(runner.filters, want) {
		t.Fatalf("altered replay changed kernel limits: %v", runner.filters)
	}
}
