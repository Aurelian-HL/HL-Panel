package runtime

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/engine"
	"github.com/hongle/hl-panel/internal/agent/reconciler"
	"github.com/hongle/hl-panel/internal/agent/state"
	usageagent "github.com/hongle/hl-panel/internal/agent/usage"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestEngineRestartPreservesFinalUsageBeforeClearingStop(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := state.NewAtomicStateStore(filepath.Join(dir, "state.json"), filepath.Join(dir, "configs"))
	adapter, err := engine.NewDryRunFileAdapter(filepath.Join(dir, "staged.json"), filepath.Join(dir, "active.json"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := reconciler.New(s, adapter, nil, "credential")
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"schema_version":1,"fragments":[]}`)
	desired := agentv1.DesiredNodeConfig{Generation: 1, Engine: agentv1.EngineNodeBundle, Config: raw, ConfigSHA256: generations.SHA256Hex(raw)}
	if err := r.Apply(ctx, desired); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEngineStopped(true); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	source := &finalCounter{invalid: true}
	journal := usageagent.NewJournalStore(filepath.Join(dir, "usage.json"))
	reporter, err := usageagent.NewReporter(journal, source, finalSender{}, "node-one", "credential", 10, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{state: s, reconciler: r, usageReporter: reporter, logger: slog.Default()}
	now = now.Add(time.Second)
	if err := a.restartEngine(ctx); !errors.Is(err, errUsageBeforeApply) {
		t.Fatalf("unsafe restart error: %v", err)
	}
	if stopped, err := s.EngineStopped(); err != nil || !stopped {
		t.Fatalf("failed accounting cleared stop: %v, %v", stopped, err)
	}
	source.invalid = false
	if err := a.restartEngine(ctx); err != nil {
		t.Fatal(err)
	}
	if stopped, err := s.EngineStopped(); err != nil || stopped {
		t.Fatalf("successful restart retained stop: %v, %v", stopped, err)
	}
	pending, err := journal.Load()
	if err != nil || len(pending.Pending) != 1 || pending.Pending[0].RuleActualBytes != 10 {
		t.Fatalf("final counters not preserved: %v", err)
	}
}
