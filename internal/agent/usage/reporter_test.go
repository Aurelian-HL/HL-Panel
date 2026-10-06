package usage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type sourceStub struct {
	deltas []CounterDelta
	err    error
	calls  int
	window CollectionWindow
}

type checkpointSourceStub struct {
	sourceStub
	begins    int
	commits   int
	rollbacks int
	active    bool
}

func (source *checkpointSourceStub) BeginCollection() error {
	if source.active {
		return errors.New("checkpoint already active")
	}
	source.begins++
	source.active = true
	return nil
}

func (source *checkpointSourceStub) CommitCollection() {
	source.commits++
	source.active = false
}

func (source *checkpointSourceStub) RollbackCollection() {
	source.rollbacks++
	source.active = false
}

func (source *sourceStub) CollectUsage(_ context.Context, window CollectionWindow) ([]CounterDelta, error) {
	source.calls++
	source.window = window
	return append([]CounterDelta(nil), source.deltas...), source.err
}

type senderStub struct {
	reports   []agentv1.UsageReport
	failCount int
}

func (sender *senderStub) ReportUsage(_ context.Context, _ string, report agentv1.UsageReport) (agentv1.UsageAcknowledgement, error) {
	sender.reports = append(sender.reports, report)
	if sender.failCount > 0 {
		sender.failCount--
		return agentv1.UsageAcknowledgement{}, errors.New("temporary network failure")
	}
	return agentv1.UsageAcknowledgement{NodeID: report.NodeID, BootID: report.BootID, Sequence: report.Sequence}, nil
}

func validDelta() CounterDelta {
	return CounterDelta{
		CustomerID: "customer-one", RuleID: "rule-one", EntryGroupID: "entry-one", Protocol: "tcp", RuleActualBytes: 100,
		CustomerActualBytes: 100, EntryMultiplierMicros: agentv1.UsageMultiplierScale,
		ExitMultiplierMicros: agentv1.UsageMultiplierScale,
	}
}

func TestReporterPersistsBootSequenceAndRetriesIdenticalPayload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage", "journal.json")
	store := NewJournalStore(path)
	current := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	now := func() time.Time { return current }
	source := &sourceStub{deltas: []CounterDelta{validDelta()}}
	failedSender := &senderStub{failCount: 1}
	reporter, err := NewReporter(store, source, failedSender, "node-one", "credential", 100, now)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	current = current.Add(time.Minute)
	if err := reporter.FlushOnce(context.Background()); err == nil {
		t.Fatal("network failure was not returned")
	}
	staged, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if staged.BootID != initial.BootID || staged.NextSequence != 2 || len(staged.Pending) != 1 || staged.Pending[0].Sequence != 1 {
		t.Fatalf("unexpected staged journal: %#v", staged)
	}
	firstPayload := failedSender.reports[0]

	// A process restart must preserve the boot ID and send the journaled payload
	// before collecting a later window.
	restartedSource := &sourceStub{deltas: []CounterDelta{validDelta()}}
	successSender := &senderStub{}
	restarted, err := NewReporter(NewJournalStore(path), restartedSource, successSender, "node-one", "credential", 100, now)
	if err != nil {
		t.Fatal(err)
	}
	current = current.Add(time.Minute)
	if err := restarted.FlushOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if restartedSource.calls != 0 {
		t.Fatal("reporter collected new counters before flushing durable pending payload")
	}
	if len(successSender.reports) != 1 || !reflect.DeepEqual(successSender.reports[0], firstPayload) {
		t.Fatalf("retry payload changed:\nfirst=%#v\nretry=%#v", firstPayload, successSender.reports)
	}
	afterRetry, err := store.Load()
	if err != nil || len(afterRetry.Pending) != 0 || afterRetry.BootID != initial.BootID || afterRetry.NextSequence != 2 {
		t.Fatalf("journal after retry = %#v, error=%v", afterRetry, err)
	}

	current = current.Add(time.Minute)
	if err := restarted.FlushOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if restartedSource.calls != 1 || len(successSender.reports) != 2 || successSender.reports[1].Sequence != 2 || successSender.reports[1].BootID != initial.BootID {
		t.Fatalf("sequence did not advance after acknowledgement: reports=%#v calls=%d", successSender.reports, restartedSource.calls)
	}
}

func TestReporterPersistsLegacyRuleMarkerInJournalJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage", "journal.json")
	store := NewJournalStore(path)
	current := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	source := &sourceStub{deltas: []CounterDelta{{
		RuleID: "rule-legacy", LegacyRuleID: "rule-legacy", RuleActualBytes: 73, CustomerActualBytes: 73,
		EntryMultiplierMicros: agentv1.UsageMultiplierScale, ExitMultiplierMicros: agentv1.UsageMultiplierScale,
	}}}
	sender := &senderStub{failCount: 1}
	reporter, err := NewReporter(store, source, sender, "node-one", "credential", 10, func() time.Time { return current })
	if err != nil {
		t.Fatal(err)
	}
	current = current.Add(time.Minute)
	if err := reporter.FlushOnce(context.Background()); err == nil {
		t.Fatal("temporary send failure was not returned")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted Journal
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	if len(persisted.Pending) != 1 {
		t.Fatalf("persisted pending reports = %#v, want one report", persisted.Pending)
	}
	report := persisted.Pending[0]
	if report.RuleID != "rule-legacy" || report.LegacyRuleID != "rule-legacy" {
		t.Fatalf("persisted legacy identifiers = rule %q, marker %q", report.RuleID, report.LegacyRuleID)
	}
	if report.CustomerID != "" || report.EntryGroupID != "" || report.ExitGroupID != "" || report.Protocol != "" {
		t.Fatalf("persisted legacy report unexpectedly contains ownership metadata: %#v", report)
	}
	if report.RuleActualBytes != 73 || report.CustomerActualBytes != 73 || report.EntryMultiplierMicros != agentv1.UsageMultiplierScale || report.ExitMultiplierMicros != agentv1.UsageMultiplierScale {
		t.Fatalf("persisted legacy accounting fields = %#v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded agentv1.UsageReport
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.LegacyRuleID != "rule-legacy" {
		t.Fatalf("legacy marker after usage JSON round trip = %q", decoded.LegacyRuleID)
	}
	if len(sender.reports) != 1 || sender.reports[0].LegacyRuleID != "rule-legacy" {
		t.Fatalf("first send did not retain legacy marker: %#v", sender.reports)
	}
}

func TestReporterRequiresRealSourceAndDoesNotJournalInvalidCounters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	store := NewJournalStore(path)
	nowValue := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	now := func() time.Time { return nowValue }
	if _, err := NewReporter(store, nil, &senderStub{}, "node-one", "credential", 10, now); !errors.Is(err, ErrCounterSourceUnavailable) {
		t.Fatalf("nil source error = %v", err)
	}
	invalid := validDelta()
	invalid.CustomerActualBytes = -1
	reporter, err := NewReporter(store, &sourceStub{deltas: []CounterDelta{invalid}}, &senderStub{}, "node-one", "credential", 10, now)
	if err != nil {
		t.Fatal(err)
	}
	nowValue = nowValue.Add(time.Minute)
	if err := reporter.FlushOnce(context.Background()); err == nil {
		t.Fatal("invalid engine counter was accepted")
	}
	journal, err := store.Load()
	if err != nil || journal.NextSequence != 1 || len(journal.Pending) != 0 || !journal.LastCollectedAt.Equal(nowValue.Add(-time.Minute)) {
		t.Fatalf("invalid counter changed journal: %#v error=%v", journal, err)
	}
}

func TestReporterAdvancesAuthoritativeEmptyWindowWithoutSending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	current := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	store := NewJournalStore(path)
	source := &sourceStub{}
	sender := &senderStub{}
	reporter, err := NewReporter(store, source, sender, "node-one", "credential", 10, func() time.Time { return current })
	if err != nil {
		t.Fatal(err)
	}
	current = current.Add(time.Minute)
	if err := reporter.FlushOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	journal, _ := store.Load()
	if !journal.LastCollectedAt.Equal(current) || journal.NextSequence != 1 || len(sender.reports) != 0 {
		t.Fatalf("empty authoritative window was not checkpointed: %#v", journal)
	}
}

func TestReporterRollsBackCheckpointWhenCounterValidationFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage", "journal.json")
	current := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	invalid := validDelta()
	invalid.CustomerActualBytes = -1
	source := &checkpointSourceStub{sourceStub: sourceStub{deltas: []CounterDelta{invalid}}}
	reporter, err := NewReporter(NewJournalStore(path), source, &senderStub{}, "node-one", "credential", 10, func() time.Time { return current })
	if err != nil {
		t.Fatal(err)
	}
	current = current.Add(time.Minute)
	if err := reporter.FlushOnce(context.Background()); err == nil {
		t.Fatal("invalid counter was accepted")
	}
	if source.begins != 1 || source.commits != 0 || source.rollbacks != 1 || source.active {
		t.Fatalf("checkpoint lifecycle = begin %d commit %d rollback %d active %v", source.begins, source.commits, source.rollbacks, source.active)
	}
	j, err := NewJournalStore(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if j.NextSequence != 1 || len(j.Pending) != 0 || !j.LastCollectedAt.Equal(current.Add(-time.Minute)) {
		t.Fatalf("journal advanced after validation failure: %#v", j)
	}
}

func TestReporterCommitsCheckpointOnlyAfterJournalStage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage", "journal.json")
	current := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	source := &checkpointSourceStub{sourceStub: sourceStub{deltas: []CounterDelta{validDelta()}}}
	sender := &senderStub{failCount: 1}
	reporter, err := NewReporter(NewJournalStore(path), source, sender, "node-one", "credential", 10, func() time.Time { return current })
	if err != nil {
		t.Fatal(err)
	}
	current = current.Add(time.Minute)
	if err := reporter.FlushOnce(context.Background()); err == nil {
		t.Fatal("send failure was not returned")
	}
	if source.begins != 1 || source.commits != 1 || source.rollbacks != 0 || source.active {
		t.Fatalf("checkpoint lifecycle = begin %d commit %d rollback %d active %v", source.begins, source.commits, source.rollbacks, source.active)
	}
	j, err := NewJournalStore(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(j.Pending) != 1 || !j.LastCollectedAt.Equal(current) {
		t.Fatalf("staged journal = %#v", j)
	}
}
