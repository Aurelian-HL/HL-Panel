package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/usage"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type policy struct{ limit int64 }

func (item policy) UsagePolicy(_ context.Context, customerID string) (usage.CustomerPolicy, error) {
	return usage.CustomerPolicy{CustomerID: customerID, TrafficLimitBytes: item.limit}, nil
}

func TestRepositoryRunsIngestEnforcementAndRevokeLifecycle(t *testing.T) {
	clock := time.Date(2026, 10, 3, 2, 0, 0, 0, time.UTC)
	service := usage.NewService(New(), policy{limit: 10}, func() time.Time { return clock })
	report := usage.Report{
		NodeID: "node-one", BootID: "boot-one", Sequence: 1, CustomerID: "customer-one", RuleID: "rule-one",
		EntryGroupID: "entry-one", Protocol: "tcp", OccurredAt: clock,
		PeriodStartedAt: clock.Add(-time.Minute), PeriodEndedAt: clock,
		RuleActualBytes: 10, CustomerActualBytes: 10,
		EntryMultiplierMicros: usage.MultiplierScale, ExitMultiplierMicros: usage.MultiplierScale,
	}
	result, err := service.Ingest(context.Background(), report)
	if err != nil || result.Decision == nil || result.Decision.Status != usage.EnforcementPending {
		t.Fatalf("ingest result=%#v error=%v", result, err)
	}
	replayed, err := service.Ingest(context.Background(), report)
	if err != nil || !replayed.Replayed || replayed.CustomerTotals.ChargedBytes != 10 {
		t.Fatalf("replay result=%#v error=%v", replayed, err)
	}
	conflicting := report
	conflicting.RuleActualBytes++
	if _, err := service.Ingest(context.Background(), conflicting); !errors.Is(err, usage.ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}

	command, err := service.DesiredEnforcement(context.Background(), "node-one")
	if err != nil || command == nil || command.Action != agentv1.EnforcementDisableCustomer || command.RuleID != "rule-one" {
		t.Fatalf("disable command=%#v error=%v", command, err)
	}
	applied, replay, err := service.RecordEnforcementResult(context.Background(), "node-one", agentv1.EnforcementResultRequest{
		DecisionID: command.DecisionID, Action: command.Action, Revision: command.Revision, Status: agentv1.EnforcementResultSucceeded,
	})
	if err != nil || replay || applied.Status != usage.EnforcementApplied {
		t.Fatalf("apply result=%#v replay=%v error=%v", applied, replay, err)
	}

	clock = clock.Add(time.Minute)
	revoking, replay, err := service.RequestRevoke(context.Background(), "admin-one", command.DecisionID, "revoke-one")
	if err != nil || replay || revoking.Status != usage.EnforcementRevokePending || revoking.Action != agentv1.EnforcementEnableCustomer {
		t.Fatalf("revoke request=%#v replay=%v error=%v", revoking, replay, err)
	}
	enable, err := service.DesiredEnforcement(context.Background(), "node-one")
	if err != nil || enable == nil || enable.Action != agentv1.EnforcementEnableCustomer || enable.Revision != revoking.Revision {
		t.Fatalf("enable command=%#v error=%v", enable, err)
	}
	revoked, _, err := service.RecordEnforcementResult(context.Background(), "node-one", agentv1.EnforcementResultRequest{
		DecisionID: enable.DecisionID, Action: enable.Action, Revision: enable.Revision, Status: agentv1.EnforcementResultSucceeded,
	})
	if err != nil || revoked.Status != usage.EnforcementRevoked {
		t.Fatalf("revoke result=%#v error=%v", revoked, err)
	}

	query, err := service.Query(context.Background(), usage.Query{Scope: usage.ScopeSite, Page: 1, PageSize: 25})
	if err != nil || query.Total != 1 || len(query.Items) != 1 || query.Items[0].Decision == nil || query.Items[0].Decision.Status != usage.EnforcementRevoked {
		t.Fatalf("query=%#v error=%v", query, err)
	}
}

func TestRepositoryRejectsSequenceGapWithoutMutation(t *testing.T) {
	now := time.Date(2026, 10, 3, 2, 0, 0, 0, time.UTC)
	service := usage.NewService(New(), policy{}, func() time.Time { return now })
	report := usage.Report{
		NodeID: "node-one", BootID: "boot-one", Sequence: 2, CustomerID: "customer-one", RuleID: "rule-one",
		EntryGroupID: "entry-one", Protocol: "tcp", OccurredAt: now,
		PeriodStartedAt: now.Add(-time.Minute), PeriodEndedAt: now,
		EntryMultiplierMicros: usage.MultiplierScale, ExitMultiplierMicros: usage.MultiplierScale,
	}
	if _, err := service.Ingest(context.Background(), report); !errors.Is(err, usage.ErrSequenceGap) {
		t.Fatalf("expected sequence gap, got %v", err)
	}
	result, err := service.Query(context.Background(), usage.Query{Scope: usage.ScopeSite})
	if err != nil || result.Total != 0 {
		t.Fatalf("gap mutated repository: result=%#v error=%v", result, err)
	}
}

func TestLegacyReplaySurvivesCurrentRuleMetadataChanges(t *testing.T) {
	repository := New()
	ended := time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC)
	report := usage.Report{
		NodeID: "node-one", BootID: "boot-one", Sequence: 1, CustomerID: "customer-one", RuleID: "rule-one",
		EntryGroupID: "entry-one", ExitGroupID: "exit-one", Protocol: "tcp", OccurredAt: ended,
		PeriodStartedAt: ended.Add(-time.Minute), PeriodEndedAt: ended, RuleActualBytes: 42, CustomerActualBytes: 42,
		EntryMultiplierMicros: usage.MultiplierScale, ExitMultiplierMicros: usage.MultiplierScale,
	}
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	event := usage.Event{Report: report, ChargedBytes: 42, PayloadSHA256: hex.EncodeToString(digest[:]), ReceivedAt: ended}
	if _, err := repository.Ingest(context.Background(), event, nil); err != nil {
		t.Fatal(err)
	}
	legacy := usage.Report{
		NodeID: "node-one", BootID: "boot-one", Sequence: 1, LegacyRuleID: "rule-one", RuleID: "rule-one",
		OccurredAt: ended, PeriodStartedAt: ended.Add(-time.Minute), PeriodEndedAt: ended,
		RuleActualBytes: 42, CustomerActualBytes: 42,
		EntryMultiplierMicros: usage.MultiplierScale, ExitMultiplierMicros: usage.MultiplierScale,
	}
	result, replayed, err := repository.ReplayLegacy(context.Background(), legacy)
	if err != nil || !replayed || !result.Replayed || result.Event.CustomerID != "customer-one" || result.CustomerTotals.ActualBytes != 42 {
		t.Fatalf("legacy replay result=%#v replayed=%v error=%v", result, replayed, err)
	}
	legacy.RuleActualBytes++
	if _, replayed, err := repository.ReplayLegacy(context.Background(), legacy); !errors.Is(err, usage.ErrIdempotencyConflict) || replayed {
		t.Fatalf("changed legacy payload replayed=%v error=%v", replayed, err)
	}
}
