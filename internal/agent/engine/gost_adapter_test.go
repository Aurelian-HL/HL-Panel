package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hongle/hl-panel/internal/control/forwarding/gostconfig"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

const gostServiceA = `{"services":[{"name":"forward-a","addr":"127.0.0.1:18081","handler":{"type":"tcp"},"listener":{"type":"tcp"},"metadata":{"enableStats":true},"forwarder":{"nodes":[{"name":"target-1","addr":"127.0.0.1:19081"}],"selector":{"strategy":"round","maxFails":1,"failTimeout":30000000000}}}]}`

func gostFragment(id, config string) agentv1.ConfigurationFragment {
	return agentv1.ConfigurationFragment{GroupID: id, GroupRevision: 1, Engine: agentv1.EngineGOST, Config: json.RawMessage(config)}
}

func newTestGOSTAdapter(t *testing.T, runner GOSTRunner, autoStart bool) *GOSTProcessAdapter {
	t.Helper()
	root := t.TempDir()
	adapter, err := NewGOSTProcessAdapter(GOSTAdapterOptions{
		StagingDir: filepath.Join(root, "staging"), ActivePath: filepath.Join(root, "active", "config.json"),
		Runner: runner, AutoStart: autoStart,
	})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func TestCompileGOSTBundleRejectsMixedAndConflictingFragments(t *testing.T) {
	second := strings.ReplaceAll(strings.ReplaceAll(gostServiceA, "forward-a", "forward-b"), "18081", "18082")
	tests := []struct {
		name      string
		fragments []agentv1.ConfigurationFragment
		want      error
	}{
		{"mixed xray", []agentv1.ConfigurationFragment{gostFragment("a", gostServiceA), {GroupID: "xray", GroupRevision: 1, Engine: agentv1.EngineXray, Config: json.RawMessage(`{"inbounds":[]}`)}}, ErrGOSTUnsupportedFragment},
		{"duplicate name", []agentv1.ConfigurationFragment{gostFragment("a", gostServiceA), gostFragment("b", gostServiceA)}, ErrGOSTConfigConflict},
		{"unknown field", []agentv1.ConfigurationFragment{gostFragment("a", `{"services":[],"api":"127.0.0.1:1234"}`)}, ErrInvalidConfiguration},
		{"incomplete service", []agentv1.ConfigurationFragment{gostFragment("a", `{"services":[{"name":"bad"}]}`)}, ErrInvalidConfiguration},
		{"trailing value", []agentv1.ConfigurationFragment{gostFragment("a", `{"services":[]} {"services":[]}`)}, ErrInvalidConfiguration},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CompileGOSTBundle(agentv1.ConfigurationBundle{SchemaVersion: 1, Fragments: tt.fragments})
			if !errors.Is(err, tt.want) {
				t.Fatalf("CompileGOSTBundle() error = %v, want %v", err, tt.want)
			}
		})
	}
	compiled, err := CompileGOSTBundle(agentv1.ConfigurationBundle{SchemaVersion: 1, Fragments: []agentv1.ConfigurationFragment{gostFragment("b", second), gostFragment("a", gostServiceA)}})
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Services []struct {
			Name string `json:"name"`
		} `json:"services"`
	}
	if err := json.Unmarshal(compiled, &document); err != nil || len(document.Services) != 2 || document.Services[0].Name != "forward-a" {
		t.Fatalf("merged services = %+v, err = %v", document.Services, err)
	}
}

func TestCompileGOSTBundlePreservesLoopbackStatsAPI(t *testing.T) {
	compiled, err := CompileGOSTBundle(agentv1.ConfigurationBundle{SchemaVersion: 1, Fragments: []agentv1.ConfigurationFragment{gostFragment("a", gostServiceA)}})
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Metrics gostconfig.Metrics `json:"metrics"`
	}
	if err := json.Unmarshal(compiled, &document); err != nil {
		t.Fatal(err)
	}
	if document.Metrics.Addr != gostconfig.StatsAPIAddress {
		t.Fatalf("compiled GOST metrics address = %q, want %q", document.Metrics.Addr, gostconfig.StatsAPIAddress)
	}
	fragment := gostFragment("b", strings.Replace(gostServiceA, "forward-a", "forward-b", 1))
	fragment.Config = json.RawMessage(strings.Replace(string(fragment.Config), `{"services":`, `{"metrics":{"addr":"0.0.0.0:18090"},"services":`, 1))
	if _, err := CompileGOSTBundle(agentv1.ConfigurationBundle{SchemaVersion: 1, Fragments: []agentv1.ConfigurationFragment{fragment}}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("non-loopback GOST API accepted: %v", err)
	}
}

func TestGOSTProcessAdapterLifecycleRollbackAndExplicitStart(t *testing.T) {
	runner := &recordingGOSTRunner{}
	adapter := newTestGOSTAdapter(t, runner, true)
	first, err := adapter.Prepare(context.Background(), testDesired(t, 1, []agentv1.ConfigurationFragment{gostFragment("a", gostServiceA)}))
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Validate(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Commit(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Verify(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	secondConfig := strings.ReplaceAll(strings.ReplaceAll(gostServiceA, "forward-a", "forward-b"), "18081", "18082")
	second, err := adapter.Prepare(context.Background(), testDesired(t, 2, []agentv1.ConfigurationFragment{gostFragment("b", secondConfig)}))
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Commit(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Verify(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if runner.processes[0].Running() || len(runner.processes) != 2 {
		t.Fatal("previous process was not replaced")
	}
	if err := adapter.Rollback(context.Background(), &first); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Verify(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Close(context.Background()); err != nil || runner.processes[2].Running() {
		t.Fatalf("Close() error = %v", err)
	}
	if err := adapter.Rollback(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(adapter.activePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("active file after rollback(nil): %v", err)
	}

	noStart := &recordingGOSTRunner{}
	dry := newTestGOSTAdapter(t, noStart, false)
	prepared, err := dry.Prepare(context.Background(), testDesired(t, 1, []agentv1.ConfigurationFragment{gostFragment("a", gostServiceA)}))
	if err != nil {
		t.Fatal(err)
	}
	if err := dry.Commit(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	if err := dry.Verify(context.Background(), prepared); err != nil || len(noStart.processes) != 0 {
		t.Fatalf("explicit no-start mode launched a process: err=%v starts=%d", err, len(noStart.processes))
	}
}

func TestGOSTProcessAdapterStartFailureRestoresPrevious(t *testing.T) {
	runner := &recordingGOSTRunner{}
	adapter := newTestGOSTAdapter(t, runner, true)
	first, err := adapter.Prepare(context.Background(), testDesired(t, 1, []agentv1.ConfigurationFragment{gostFragment("a", gostServiceA)}))
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Commit(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	previous, err := os.ReadFile(adapter.activePath)
	if err != nil {
		t.Fatal(err)
	}
	runner.startErrors = []error{errors.New("candidate failed")}
	secondConfig := strings.ReplaceAll(strings.ReplaceAll(gostServiceA, "forward-a", "forward-b"), "18081", "18082")
	second, err := adapter.Prepare(context.Background(), testDesired(t, 2, []agentv1.ConfigurationFragment{gostFragment("b", secondConfig)}))
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Commit(context.Background(), second); !errors.Is(err, ErrGOSTStart) {
		t.Fatalf("Commit(second) error = %v", err)
	}
	if err := adapter.Verify(context.Background(), first); err != nil {
		t.Fatalf("last-known-good process not restored: %v", err)
	}
	active, err := os.ReadFile(adapter.activePath)
	if err != nil || !bytes.Equal(active, previous) || len(runner.processes) != 2 {
		t.Fatalf("last-known-good file/process not restored: err=%v starts=%d", err, len(runner.processes))
	}
}

func TestGOSTProcessAdapterValidationAndLiveness(t *testing.T) {
	runner := &recordingGOSTRunner{testErr: errors.New("sensitive engine diagnostic")}
	adapter := newTestGOSTAdapter(t, runner, true)
	prepared, err := adapter.Prepare(context.Background(), testDesired(t, 1, []agentv1.ConfigurationFragment{gostFragment("a", gostServiceA)}))
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Validate(context.Background(), prepared); !errors.Is(err, ErrGOSTValidation) || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("Validate() error leaked runner diagnostic: %v", err)
	}
	runner.testErr = nil
	if err := os.WriteFile(prepared.adapterPath, []byte(`{"services":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Validate(context.Background(), prepared); !errors.Is(err, ErrConfigurationHash) {
		t.Fatalf("tampered staged config accepted: %v", err)
	}
	if _, err := adapter.Prepare(context.Background(), prepared.DesiredNodeConfig()); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Commit(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	runner.processes[0].setRunning(false)
	if err := adapter.Verify(context.Background(), prepared); !errors.Is(err, ErrGOSTNotRunning) {
		t.Fatalf("dead process accepted: %v", err)
	}
}

type recordingGOSTRunner struct {
	testErr     error
	startErrors []error
	processes   []*recordingGOSTProcess
}

func (r *recordingGOSTRunner) Test(context.Context, string) error { return r.testErr }

func (r *recordingGOSTRunner) Start(context.Context, string) (GOSTProcess, error) {
	if len(r.startErrors) != 0 {
		err := r.startErrors[0]
		r.startErrors = r.startErrors[1:]
		return nil, err
	}
	process := &recordingGOSTProcess{running: true}
	r.processes = append(r.processes, process)
	return process, nil
}

type recordingGOSTProcess struct {
	mu      sync.Mutex
	running bool
}

func (p *recordingGOSTProcess) Stop(context.Context) error {
	p.setRunning(false)
	return nil
}

func (p *recordingGOSTProcess) Running() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

func (p *recordingGOSTProcess) setRunning(value bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.running = value
}
