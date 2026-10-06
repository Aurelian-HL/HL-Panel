package usage

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

type stubRepository struct {
	events      []Event
	resultTotal CustomerTotals
	query       Query
	queryResult QueryResult
}

type replayRepository struct{ result IngestResult }

func (repository replayRepository) Ingest(context.Context, Event, DecisionProjector) (IngestResult, error) {
	return repository.result, nil
}

func (replayRepository) Query(context.Context, Query) (QueryResult, error) { return QueryResult{}, nil }
func (replayRepository) DesiredEnforcement(context.Context, string) (*EnforcementDecision, error) {
	return nil, nil
}
func (replayRepository) RecordEnforcementResult(context.Context, EnforcementResult) (EnforcementDecision, bool, error) {
	return EnforcementDecision{}, false, nil
}
func (replayRepository) RequestRevoke(context.Context, RevokeRequest) (EnforcementDecision, bool, error) {
	return EnforcementDecision{}, false, nil
}

func (repository *stubRepository) Ingest(_ context.Context, event Event, project DecisionProjector) (IngestResult, error) {
	repository.events = append(repository.events, event)
	totals := repository.resultTotal
	if totals.CustomerID == "" {
		totals.CustomerID = event.CustomerID
	}
	decision, err := project(totals)
	return IngestResult{Event: event, CustomerTotals: totals, Decision: decision}, err
}

func (repository *stubRepository) Query(_ context.Context, query Query) (QueryResult, error) {
	repository.query = query
	return repository.queryResult, nil
}
func (repository *stubRepository) DesiredEnforcement(context.Context, string) (*EnforcementDecision, error) {
	return nil, nil
}
func (repository *stubRepository) RecordEnforcementResult(context.Context, EnforcementResult) (EnforcementDecision, bool, error) {
	return EnforcementDecision{}, false, nil
}
func (repository *stubRepository) RequestRevoke(context.Context, RevokeRequest) (EnforcementDecision, bool, error) {
	return EnforcementDecision{}, false, nil
}

type stubPolicies struct {
	policy CustomerPolicy
	err    error
}

func (policies stubPolicies) UsagePolicy(context.Context, string) (CustomerPolicy, error) {
	return policies.policy, policies.err
}

func testReport() Report {
	return Report{
		NodeID: "node-one", BootID: "boot-one", Sequence: 1, CustomerID: "customer-one", RuleID: "rule-one", Protocol: "tcp",
		EntryGroupID: "entry-one", ExitGroupID: "exit-one", OccurredAt: time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC),
		PeriodStartedAt: time.Date(2026, 10, 3, 0, 59, 0, 0, time.UTC),
		PeriodEndedAt:   time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC), RuleActualBytes: 1,
		CustomerActualBytes: 1, EntryMultiplierMicros: 1_500_000, ExitMultiplierMicros: 1_000_000,
	}
}

func TestIngestCalculatesChargedBytesAndPendingQuotaDecision(t *testing.T) {
	now := time.Date(2026, 10, 3, 2, 0, 0, 0, time.UTC)
	repository := &stubRepository{resultTotal: CustomerTotals{CustomerID: "customer-one", ActualBytes: 8, ChargedBytes: 11}}
	service := NewService(repository, stubPolicies{policy: CustomerPolicy{CustomerID: "customer-one", TrafficLimitBytes: 10}}, func() time.Time { return now })

	result, err := service.Ingest(context.Background(), testReport())
	if err != nil {
		t.Fatal(err)
	}
	if result.Event.ChargedBytes != 2 {
		t.Fatalf("charged bytes = %d, want per-event ceiling of 2", result.Event.ChargedBytes)
	}
	if len(result.Event.PayloadSHA256) != 64 {
		t.Fatal("missing SHA-256 idempotency fingerprint")
	}
	if result.Decision == nil || result.Decision.Reason != EnforcementQuotaExhausted || result.Decision.Status != EnforcementPending {
		t.Fatalf("unexpected quota projection: %#v", result.Decision)
	}
	if result.Decision.Action != "disable_customer_access" || result.Decision.CustomerChargedBytes != 11 {
		t.Fatal("enforcement projection did not preserve the post-ingest usage snapshot")
	}
}

func TestIngestNormalizesEquivalentTimestampsBeforeFingerprinting(t *testing.T) {
	repository := &stubRepository{resultTotal: CustomerTotals{CustomerID: "customer-one"}}
	service := NewService(repository, stubPolicies{policy: CustomerPolicy{CustomerID: "customer-one"}}, time.Now)
	first := testReport()
	second := first
	zone := time.FixedZone("UTC+8", 8*60*60)
	second.OccurredAt = first.OccurredAt.In(zone)
	second.PeriodStartedAt = first.PeriodStartedAt.In(zone)
	second.PeriodEndedAt = first.PeriodEndedAt.In(zone)

	if _, err := service.Ingest(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Ingest(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if repository.events[0].PayloadSHA256 != repository.events[1].PayloadSHA256 {
		t.Fatal("equivalent instants produced different idempotency fingerprints")
	}
}

func TestReplayDoesNotDependOnCurrentCustomerPolicy(t *testing.T) {
	expected := IngestResult{Event: Event{Report: testReport()}, Replayed: true}
	service := NewService(replayRepository{result: expected}, stubPolicies{err: errors.New("customer was removed")}, time.Now)
	result, err := service.Ingest(context.Background(), testReport())
	if err != nil || !result.Replayed {
		t.Fatalf("replay unexpectedly depended on current customer policy: result=%#v error=%v", result, err)
	}
}

func TestIngestRejectsNegativeBytesInvalidMultiplierAndOverflow(t *testing.T) {
	repository := &stubRepository{}
	service := NewService(repository, stubPolicies{policy: CustomerPolicy{CustomerID: "customer-one"}}, time.Now)
	tests := []struct {
		name   string
		mutate func(*Report)
		want   error
	}{
		{name: "negative", mutate: func(report *Report) { report.RuleActualBytes = -1 }, want: ErrNegativeBytes},
		{name: "zero multiplier", mutate: func(report *Report) { report.EntryMultiplierMicros = 0 }, want: ErrInvalidMultiplier},
		{name: "overflow", mutate: func(report *Report) {
			report.CustomerActualBytes = math.MaxInt64
			report.EntryMultiplierMicros = MaxMultiplierMicros
			report.ExitMultiplierMicros = MaxMultiplierMicros
		}, want: ErrByteOverflow},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := testReport()
			test.mutate(&report)
			if _, err := service.Ingest(context.Background(), report); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
	if len(repository.events) != 0 {
		t.Fatal("invalid report reached repository")
	}
}

func TestEnforcementPriorityIsDisabledThenExpiredThenQuota(t *testing.T) {
	now := time.Date(2026, 10, 3, 2, 0, 0, 0, time.UTC)
	expired := now.Add(-time.Minute)
	event := Event{Report: testReport()}
	totals := CustomerTotals{CustomerID: "customer-one", ChargedBytes: 100}
	tests := []struct {
		policy CustomerPolicy
		want   EnforcementReason
	}{
		{policy: CustomerPolicy{CustomerID: "customer-one", Disabled: true, ExpiresAt: &expired, TrafficLimitBytes: 1}, want: EnforcementCustomerDisabled},
		{policy: CustomerPolicy{CustomerID: "customer-one", ExpiresAt: &expired, TrafficLimitBytes: 1}, want: EnforcementCustomerExpired},
		{policy: CustomerPolicy{CustomerID: "customer-one", TrafficLimitBytes: 100}, want: EnforcementQuotaExhausted},
	}
	for _, test := range tests {
		decision := projectEnforcement(test.policy, totals, event, now)
		if decision == nil || decision.Reason != test.want {
			t.Fatalf("decision = %#v, want %s", decision, test.want)
		}
	}
	if decision := projectEnforcement(CustomerPolicy{CustomerID: "customer-one", TrafficLimitBytes: 101}, totals, event, now); decision != nil {
		t.Fatal("active customer below quota received enforcement decision")
	}
}

func TestQueryDefaultsAndValidatesScopeTimeAndPagination(t *testing.T) {
	repository := &stubRepository{queryResult: QueryResult{Items: nil, Total: 3}}
	service := NewService(repository, stubPolicies{}, time.Now)
	result, err := service.Query(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if repository.query.Scope != ScopeSite || repository.query.Page != 1 || repository.query.PageSize != 50 {
		t.Fatalf("unexpected defaults: %#v", repository.query)
	}
	if result.Items == nil {
		t.Fatal("empty result must encode as [] instead of null")
	}
	from := time.Now()
	to := from.Add(-time.Second)
	invalid := []Query{
		{Scope: "unknown"},
		{Scope: ScopeCustomer},
		{Scope: ScopeSite, ScopeID: "not-allowed"},
		{Scope: ScopeSite, From: &from, To: &to},
		{Scope: ScopeSite, Page: 1, PageSize: maxPageSize + 1},
	}
	for _, query := range invalid {
		if _, err := service.Query(context.Background(), query); err == nil {
			t.Fatalf("invalid query accepted: %#v", query)
		}
	}
}
