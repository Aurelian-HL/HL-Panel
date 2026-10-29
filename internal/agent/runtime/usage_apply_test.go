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

type finalCounter struct{ invalid bool }

func (s *finalCounter) CollectUsage(context.Context, usageagent.CollectionWindow) ([]usageagent.CounterDelta, error) {
	n := int64(10)
	if s.invalid {
		n = -1
	}
	return []usageagent.CounterDelta{{RuleID: "rule-one", LegacyRuleID: "rule-one", RuleActualBytes: n, CustomerActualBytes: n}}, nil
}

type finalSender struct{}

func (finalSender) ReportUsage(_ context.Context, _ string, r agentv1.UsageReport) (agentv1.UsageAcknowledgement, error) {
	return agentv1.UsageAcknowledgement{NodeID: r.NodeID, BootID: r.BootID, Sequence: r.Sequence}, nil
}

func TestConfigurationSwitchWaitsForDurableFinalUsageAndCanRetry(t *testing.T) {
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
	now := time.Now().UTC()
	source := &finalCounter{invalid: true}
	journal := usageagent.NewJournalStore(filepath.Join(dir, "usage.json"))
	reporter, err := usageagent.NewReporter(journal, source, finalSender{}, "node-one", "credential", 10, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{state: s, reconciler: r, usageReporter: reporter, logger: slog.Default()}
	desired.Generation = 2
	now = now.Add(time.Second)
	if err := a.applyWithUsage(ctx, desired); !errors.Is(err, errUsageBeforeApply) {
		t.Fatalf("unsafe switch error: %v", err)
	}
	current, _ := s.Load()
	if current.Applied.Generation != 1 || current.Pending != nil {
		t.Fatal("configuration changed before final accounting was durable")
	}
	source.invalid = false
	if err := a.applyWithUsage(ctx, desired); err != nil {
		t.Fatal(err)
	}
	current, _ = s.Load()
	pending, _ := journal.Load()
	if current.Applied.Generation != 2 || len(pending.Pending) != 1 || pending.Pending[0].RuleActualBytes != 10 {
		t.Fatal("retry did not preserve old counters before switch")
	}
}

func TestUsageGenerationPreservesFinalRuleRemovalInDurableJournal(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
		want agentv1.NodeConfigGeneration
	}{
		{"last rule removed", `{"schema_version":1,"fragments":[]}`, 7},
		{"metadata-bearing rule", `{"schema_version":1,"fragments":[{"usage":{"rule_id":"rule-one"}}]}`, 7},
		{"legacy rule without metadata", `{"schema_version":1,"fragments":[{"group_id":"legacy"}]}`, 0},
		{"unknown empty schema", `{"schema_version":2,"fragments":[]}`, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir, now := t.TempDir(), time.Now().UTC()
			s := state.NewAtomicStateStore(filepath.Join(dir, "state.json"), filepath.Join(dir, "configs"))
			desired := agentv1.DesiredNodeConfig{Generation: 7, Engine: agentv1.EngineNodeBundle,
				Config: []byte(tt.raw), ConfigSHA256: generations.SHA256Hex([]byte(tt.raw))}
			if _, err := s.Begin(desired, now); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Complete(desired, now); err != nil {
				t.Fatal(err)
			}
			a := &Agent{state: s}
			journal := usageagent.NewJournalStore(filepath.Join(dir, "usage.json"))
			reporter, err := usageagent.NewReporter(journal, &finalCounter{}, finalSender{}, "node-one", "credential", 10, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			reporter.SetGenerationProvider(a.usageGeneration)
			for range 2 {
				now = now.Add(time.Second)
				if err := reporter.CollectOnce(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			pending, err := journal.Load()
			if err != nil || len(pending.Pending) != 2 {
				t.Fatal("tail was not persisted before delivery", err)
			}
			for i, report := range pending.Pending {
				if report.ConfigGeneration != tt.want || report.Sequence != int64(i+1) || report.RuleActualBytes != 10 {
					t.Fatalf("generation, order or byte count changed: %+v", report)
				}
			}
			if err := reporter.DeliverPending(context.Background()); err != nil {
				t.Fatal(err)
			}
			pending, err = journal.Load()
			if err != nil || len(pending.Pending) != 0 {
				t.Fatal("acknowledged journal did not drain", err)
			}
		})
	}
}
