package runtime

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/engine"
	"github.com/hongle/hl-panel/internal/agent/reconciler"
	"github.com/hongle/hl-panel/internal/agent/state"
	usageagent "github.com/hongle/hl-panel/internal/agent/usage"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type blockedUsageSender struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockedUsageSender) ReportUsage(ctx context.Context, _ string, report agentv1.UsageReport) (agentv1.UsageAcknowledgement, error) {
	s.once.Do(func() { close(s.started) })
	select {
	case <-ctx.Done():
		return agentv1.UsageAcknowledgement{}, ctx.Err()
	case <-s.release:
		return agentv1.UsageAcknowledgement{}, errors.New("control plane unavailable")
	}
}

func TestSlowUsageDeliveryDoesNotBlockConfigurationAndLocalRecovery(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := state.NewAtomicStateStore(filepath.Join(dir, "state.json"), filepath.Join(dir, "configs"))
	adapter, err := engine.NewDryRunFileAdapter(filepath.Join(dir, "staged.json"), filepath.Join(dir, "active.json"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := reconciler.New(store, adapter, nil, "credential")
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"schema_version":1,"fragments":[]}`)
	desired := agentv1.DesiredNodeConfig{Generation: 1, Engine: agentv1.EngineNodeBundle, Config: raw, ConfigSHA256: generations.SHA256Hex(raw)}
	if err := r.Apply(ctx, desired); err != nil {
		t.Fatal(err)
	}
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	sender := &blockedUsageSender{started: make(chan struct{}), release: make(chan struct{})}
	journal := usageagent.NewJournalStore(filepath.Join(dir, "usage.json"))
	reporter, err := usageagent.NewReporter(journal, &finalCounter{}, sender, "node-one", "credential", 10, now)
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{state: store, reconciler: r, usageReporter: reporter, logger: slog.Default()}
	clock.Add(int64(time.Second))
	flushDone := make(chan error, 1)
	var lifecycleDone chan error
	lifecycleFinished := false
	go func() { flushDone <- a.flushUsage(ctx) }()
	defer func() {
		close(sender.release)
		if err := <-flushDone; err == nil {
			t.Error("blocked delivery did not retain its failure")
		}
		if lifecycleDone != nil && !lifecycleFinished {
			<-lifecycleDone
		}
	}()
	select {
	case <-sender.started:
	case <-time.After(time.Second):
		t.Fatal("usage delivery did not start")
	}
	clock.Add(int64(time.Second))
	desired.Generation = 2
	lifecycleDone = make(chan error, 1)
	go func() {
		if err := a.applyWithUsage(ctx, desired); err != nil {
			lifecycleDone <- err
			return
		}
		lifecycleDone <- a.recoverEngine(ctx)
	}()
	select {
	case err := <-lifecycleDone:
		lifecycleFinished = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("network delivery blocked local configuration and recovery")
	}
	current, err := store.Load()
	if err != nil || current.AppliedGeneration() != 2 {
		t.Fatalf("configuration not applied: %v", err)
	}
	pending, err := journal.Load()
	if err != nil || len(pending.Pending) != 2 || pending.Pending[0].Sequence != 1 || pending.Pending[1].Sequence != 2 {
		t.Fatalf("failed delivery or final old-process counters were lost: %v", err)
	}
}

type gatedUsageApplyAdapter struct {
	engine.EngineAdapter
	started chan struct{}
	release chan struct{}
}

func (a *gatedUsageApplyAdapter) Commit(ctx context.Context, prepared engine.PreparedConfiguration) error {
	if prepared.Generation == 2 {
		close(a.started)
		select {
		case <-a.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return a.EngineAdapter.Commit(ctx, prepared)
}

type secondWindowCounter struct {
	calls atomic.Int32
}

func (s *secondWindowCounter) CollectUsage(context.Context, usageagent.CollectionWindow) ([]usageagent.CounterDelta, error) {
	if s.calls.Add(1) == 1 {
		return nil, nil // No pending payload may hide a concurrent collection.
	}
	return (&finalCounter{}).CollectUsage(context.Background(), usageagent.CollectionWindow{})
}

type capturedUsageSender struct{ reports chan agentv1.UsageReport }

func (s capturedUsageSender) ReportUsage(_ context.Context, _ string, report agentv1.UsageReport) (agentv1.UsageAcknowledgement, error) {
	s.reports <- report
	return finalSender{}.ReportUsage(context.Background(), "", report)
}

func TestUsageWindowCannotCrossConfigurationCommit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dir := t.TempDir()
	store := state.NewAtomicStateStore(filepath.Join(dir, "state.json"), filepath.Join(dir, "configs"))
	fileAdapter, err := engine.NewDryRunFileAdapter(filepath.Join(dir, "staged.json"), filepath.Join(dir, "active.json"))
	if err != nil {
		t.Fatal(err)
	}
	adapter := &gatedUsageApplyAdapter{EngineAdapter: fileAdapter, started: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	var workers sync.WaitGroup
	defer func() {
		release.Do(func() { close(adapter.release) })
		cancel()
		workers.Wait()
	}()
	r, err := reconciler.New(store, adapter, nil, "credential")
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"schema_version":1,"fragments":[]}`)
	desired := agentv1.DesiredNodeConfig{Generation: 1, Engine: agentv1.EngineNodeBundle, Config: raw, ConfigSHA256: generations.SHA256Hex(raw)}
	if err := r.Apply(ctx, desired); err != nil {
		t.Fatal(err)
	}
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	counter := &secondWindowCounter{}
	sender := capturedUsageSender{reports: make(chan agentv1.UsageReport, 2)}
	reporter, err := usageagent.NewReporter(usageagent.NewJournalStore(filepath.Join(dir, "usage.json")), counter, sender, "node-one", "credential", 10, now)
	if err != nil {
		t.Fatal(err)
	}
	reporter.SetGenerationProvider(func() (agentv1.NodeConfigGeneration, error) {
		current, err := store.Load()
		return current.AppliedGeneration(), err
	})
	a := &Agent{state: store, reconciler: r, usageReporter: reporter, logger: slog.Default()}
	clock.Add(int64(time.Second))
	desired.Generation = 2
	applyDone := make(chan error, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		applyDone <- a.applyWithUsage(ctx, desired)
	}()
	select {
	case <-adapter.started:
	case <-ctx.Done():
		t.Fatal("configuration commit did not start")
	}
	clock.Add(int64(time.Second))
	flushDone := make(chan error, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		flushDone <- a.flushUsage(ctx)
	}()
	select {
	case <-sender.reports:
		t.Fatal("usage collected and delivered while configuration commit was unfinished")
	case <-time.After(100 * time.Millisecond):
	}
	release.Do(func() { close(adapter.release) })
	if err := <-applyDone; err != nil {
		t.Fatal(err)
	}
	if err := <-flushDone; err != nil {
		t.Fatal(err)
	}
	select {
	case report := <-sender.reports:
		if report.ConfigGeneration != 2 || report.RuleActualBytes != 10 {
			t.Fatalf("usage attributed to wrong process generation: generation=%d bytes=%d", report.ConfigGeneration, report.RuleActualBytes)
		}
	default:
		t.Fatal("usage was not delivered after configuration commit")
	}
}
