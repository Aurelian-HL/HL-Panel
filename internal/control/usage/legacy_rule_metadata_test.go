package usage

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type legacyRuleInputRepository struct {
	input deploymentreceipts.Input
	err   error
}

func (repository legacyRuleInputRepository) RuleNodeDeploymentInput(context.Context, string, string) (deploymentreceipts.Input, error) {
	return repository.input, repository.err
}

func TestLegacyRuleMetadataRequiresCurrentAppliedMembershipAndTag(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*deploymentreceipts.Input)
	}{
		{name: "verified current rule", mutate: func(*deploymentreceipts.Input) {}},
		{name: "node is not an active entry member", mutate: func(input *deploymentreceipts.Input) { input.ActiveMember = false }},
		{name: "node has not applied current config", mutate: func(input *deploymentreceipts.Input) { input.Node.AppliedGeneration-- }},
		{name: "legacy tag absent", mutate: func(input *deploymentreceipts.Input) {
			setLegacyTestBundle(t, input, `{"inbounds":[{"tag":"vless-reality-rule-1--new"}]}`)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := completeLegacyRuleInput(t)
			test.mutate(&input)
			provider := NewLegacyRuleMetadataProvider(legacyRuleInputRepository{input: input})
			metadata, err := provider.ResolveLegacyRuleUsageMetadata(context.Background(), "node-1", "rule-1")
			if test.name == "verified current rule" {
				if err != nil {
					t.Fatal(err)
				}
				if metadata != (LegacyRuleMetadata{RuleID: "rule-1", CustomerID: "customer-1", EntryGroup: "entry-1", ExitGroup: "exit-1", Protocol: "tcp"}) {
					t.Fatalf("resolved metadata = %#v", metadata)
				}
				return
			}
			if !errors.Is(err, faults.ErrValidation) {
				t.Fatalf("error = %v, want validation failure", err)
			}
		})
	}
}

func TestLegacyRuleMetadataRequiresCurrentAppliedGOSTService(t *testing.T) {
	tests := []struct {
		name   string
		config string
		wantOK bool
	}{
		{name: "service present", config: `{"services":[{"name":"forward-rule-1","handler":{"type":"tcp"},"listener":{"type":"tcp"},"metadata":{"enableStats":true}}]}`, wantOK: true},
		{name: "stats disabled", config: `{"services":[{"name":"forward-rule-1","handler":{"type":"tcp"},"listener":{"type":"tcp"},"metadata":{"enableStats":false}}]}`},
		{name: "stats missing", config: `{"services":[{"name":"forward-rule-1","handler":{"type":"tcp"},"listener":{"type":"tcp"},"metadata":{}}]}`},
		{name: "handler is not TCP", config: `{"services":[{"name":"forward-rule-1","handler":{"type":"socks5"},"listener":{"type":"tcp"},"metadata":{"enableStats":true}}]}`},
		{name: "listener is not TCP", config: `{"services":[{"name":"forward-rule-1","handler":{"type":"tcp"},"listener":{"type":"udp"},"metadata":{"enableStats":true}}]}`},
		{name: "service missing", config: `{"services":[]}`},
		{name: "service name mismatch", config: `{"services":[{"name":"forward-rule-2"}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := completeLegacyGOSTRuleInput(t, test.config)
			provider := NewLegacyRuleMetadataProvider(legacyRuleInputRepository{input: input})
			metadata, err := provider.ResolveLegacyRuleUsageMetadata(context.Background(), "node-1", "rule-1")
			if test.wantOK {
				if err != nil {
					t.Fatal(err)
				}
				if metadata != (LegacyRuleMetadata{RuleID: "rule-1", CustomerID: "customer-1", EntryGroup: "entry-1", ExitGroup: "exit-1", Protocol: "tcp"}) {
					t.Fatalf("resolved metadata = %#v", metadata)
				}
				return
			}
			if !errors.Is(err, faults.ErrValidation) {
				t.Fatalf("error = %v, want validation failure", err)
			}
		})
	}
}

func TestLegacyRuleMetadataRejectsUnknownRule(t *testing.T) {
	provider := NewLegacyRuleMetadataProvider(legacyRuleInputRepository{err: faults.ErrNotFound})
	if _, err := provider.ResolveLegacyRuleUsageMetadata(context.Background(), "node-1", "missing-rule"); !errors.Is(err, faults.ErrNotFound) {
		t.Fatalf("unknown rule error = %v", err)
	}
}

func TestLegacyUsageReportCannotSupplyRuleMetadata(t *testing.T) {
	provider := &countingLegacyRuleProvider{}
	repository := &stubRepository{}
	service := NewService(repository, stubPolicies{policy: CustomerPolicy{CustomerID: "customer-1"}}, time.Now,
		WithLegacyRuleMetadataProvider(provider))
	report := Report{
		NodeID: "node-1", BootID: "boot-1", Sequence: 1, LegacyRuleID: "rule-1", RuleID: "rule-1",
		CustomerID: "attacker-customer", EntryGroupID: "attacker-entry", ExitGroupID: "attacker-exit", Protocol: "tcp",
		OccurredAt: time.Now(), PeriodStartedAt: time.Now().Add(-time.Minute), PeriodEndedAt: time.Now(), RuleActualBytes: 10,
		CustomerActualBytes: 10, EntryMultiplierMicros: MultiplierScale, ExitMultiplierMicros: MultiplierScale,
	}
	if _, err := service.Ingest(context.Background(), report); !errors.Is(err, faults.ErrValidation) {
		t.Fatalf("forged metadata error = %v", err)
	}
	if provider.calls != 0 || len(repository.events) != 0 {
		t.Fatalf("forged report reached resolver or ledger: resolver calls=%d events=%d", provider.calls, len(repository.events))
	}
}

func TestIngestResolvesLegacyRuleMetadataBeforeLedgerWrite(t *testing.T) {
	metadata := LegacyRuleMetadata{RuleID: "rule-1", CustomerID: "customer-1", EntryGroup: "entry-1", ExitGroup: "exit-1", Protocol: "tcp"}
	provider := &fixedLegacyRuleProvider{metadata: metadata}
	repository := &stubRepository{}
	service := NewService(repository, stubPolicies{policy: CustomerPolicy{CustomerID: "customer-1"}}, time.Now,
		WithLegacyRuleMetadataProvider(provider))
	now := time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC)
	report := Report{
		NodeID: "node-1", BootID: "boot-1", Sequence: 1, LegacyRuleID: "rule-1", RuleID: "rule-1",
		OccurredAt: now, PeriodStartedAt: now.Add(-time.Minute), PeriodEndedAt: now,
		RuleActualBytes: 42,
	}
	if _, err := service.Ingest(context.Background(), report); err != nil {
		t.Fatal(err)
	}
	if provider.nodeID != "node-1" || provider.ruleID != "rule-1" {
		t.Fatalf("resolver received node=%q rule=%q", provider.nodeID, provider.ruleID)
	}
	if len(repository.events) != 1 {
		t.Fatalf("ledger events = %d, want 1", len(repository.events))
	}
	resolved := repository.events[0].Report
	if resolved.LegacyRuleID != "" || resolved.CustomerID != "customer-1" || resolved.EntryGroupID != "entry-1" ||
		resolved.ExitGroupID != "exit-1" || resolved.Protocol != "tcp" || resolved.CustomerActualBytes != 42 ||
		resolved.EntryMultiplierMicros != MultiplierScale || resolved.ExitMultiplierMicros != MultiplierScale {
		t.Fatalf("resolved legacy report = %#v", resolved)
	}
}

func TestLegacyReplayRunsBeforeCurrentRuleResolver(t *testing.T) {
	ended := time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC)
	expected := IngestResult{Event: Event{Report: Report{NodeID: "node-1", BootID: "boot-1", Sequence: 1}}, Replayed: true}
	repository := &legacyReplayRepositoryStub{stubRepository: &stubRepository{}, result: expected}
	provider := &countingLegacyRuleProvider{}
	service := NewService(repository, stubPolicies{policy: CustomerPolicy{CustomerID: "customer-1"}}, time.Now,
		WithLegacyRuleMetadataProvider(provider))
	input := Report{
		NodeID: "node-1", BootID: "boot-1", Sequence: 1, LegacyRuleID: "rule-1", RuleID: "rule-1",
		OccurredAt: ended, PeriodStartedAt: ended.Add(-time.Minute), PeriodEndedAt: ended,
		RuleActualBytes: 42, CustomerActualBytes: 42,
		EntryMultiplierMicros: MultiplierScale, ExitMultiplierMicros: MultiplierScale,
	}
	result, err := service.Ingest(context.Background(), input)
	if err != nil || !result.Replayed || repository.calls != 1 || provider.calls != 0 {
		t.Fatalf("replay result=%#v repo calls=%d resolver calls=%d error=%v", result, repository.calls, provider.calls, err)
	}
}

func TestLegacyReplayMatchesPostgresTimestampPrecision(t *testing.T) {
	started := time.Date(2026, 10, 6, 2, 59, 0, 987654321, time.UTC)
	ended := time.Date(2026, 10, 6, 3, 0, 0, 123456789, time.UTC)
	report := Report{
		NodeID: "node-1", BootID: "boot-1", Sequence: 1, LegacyRuleID: "rule-1", RuleID: "rule-1",
		OccurredAt: ended, PeriodStartedAt: started, PeriodEndedAt: ended,
		RuleActualBytes: 42, CustomerActualBytes: 42,
		EntryMultiplierMicros: MultiplierScale, ExitMultiplierMicros: MultiplierScale,
	}
	stored := Event{Report: Report{
		NodeID: report.NodeID, BootID: report.BootID, Sequence: report.Sequence,
		CustomerID: "customer-1", EntryGroupID: "entry-1", ExitGroupID: "exit-1",
		RuleID: report.RuleID, Protocol: "tcp", OccurredAt: ended.Truncate(time.Microsecond),
		PeriodStartedAt: started.Truncate(time.Microsecond), PeriodEndedAt: ended.Truncate(time.Microsecond),
		RuleActualBytes: report.RuleActualBytes, CustomerActualBytes: report.CustomerActualBytes,
		EntryMultiplierMicros: MultiplierScale, ExitMultiplierMicros: MultiplierScale,
	}}
	normalized, err := normalizeReport(Report{
		NodeID: report.NodeID, BootID: report.BootID, Sequence: report.Sequence,
		CustomerID: stored.CustomerID, RuleID: stored.RuleID, EntryGroupID: stored.EntryGroupID,
		ExitGroupID: stored.ExitGroupID, Protocol: stored.Protocol,
		OccurredAt: report.OccurredAt, PeriodStartedAt: report.PeriodStartedAt, PeriodEndedAt: report.PeriodEndedAt,
		RuleActualBytes: report.RuleActualBytes, CustomerActualBytes: report.RuleActualBytes,
		EntryMultiplierMicros: MultiplierScale, ExitMultiplierMicros: MultiplierScale,
	})
	if err != nil {
		t.Fatal(err)
	}
	stored.PayloadSHA256, err = reportFingerprint(normalized)
	if err != nil {
		t.Fatal(err)
	}
	if !LegacyReplayMatches(stored, report) {
		t.Fatal("PostgreSQL microsecond timestamps did not match the journaled nanosecond report")
	}
	legacyFieldsOverwrittenOnIngest := report
	legacyFieldsOverwrittenOnIngest.CustomerActualBytes = 0
	legacyFieldsOverwrittenOnIngest.EntryMultiplierMicros = 0
	legacyFieldsOverwrittenOnIngest.ExitMultiplierMicros = 0
	if !LegacyReplayMatches(stored, legacyFieldsOverwrittenOnIngest) {
		t.Fatal("legacy replay rejected fields that are replaced by control-plane metadata")
	}
	storedUDP := stored
	storedUDP.Protocol = "udp"
	udpNormalized, err := normalizeReport(Report{
		NodeID: report.NodeID, BootID: report.BootID, Sequence: report.Sequence,
		CustomerID: storedUDP.CustomerID, RuleID: storedUDP.RuleID, EntryGroupID: storedUDP.EntryGroupID,
		ExitGroupID: storedUDP.ExitGroupID, Protocol: storedUDP.Protocol,
		OccurredAt: report.OccurredAt, PeriodStartedAt: report.PeriodStartedAt, PeriodEndedAt: report.PeriodEndedAt,
		RuleActualBytes: report.RuleActualBytes, CustomerActualBytes: report.RuleActualBytes,
		EntryMultiplierMicros: MultiplierScale, ExitMultiplierMicros: MultiplierScale,
	})
	if err != nil {
		t.Fatal(err)
	}
	storedUDP.PayloadSHA256, err = reportFingerprint(udpNormalized)
	if err != nil {
		t.Fatal(err)
	}
	if !LegacyReplayMatches(storedUDP, report) {
		t.Fatal("UDP legacy rule replay was rejected")
	}
	sameMicrosecondDifferentNanosecond := report
	sameMicrosecondDifferentNanosecond.PeriodEndedAt = sameMicrosecondDifferentNanosecond.PeriodEndedAt.Add(time.Nanosecond)
	if LegacyReplayMatches(stored, sameMicrosecondDifferentNanosecond) {
		t.Fatal("different nanosecond report within the same PostgreSQL microsecond was accepted as a replay")
	}
	report.PeriodEndedAt = report.PeriodEndedAt.Add(time.Microsecond)
	if LegacyReplayMatches(stored, report) {
		t.Fatal("different PostgreSQL timestamp was accepted as an idempotent replay")
	}
}

type countingLegacyRuleProvider struct{ calls int }

func (provider *countingLegacyRuleProvider) ResolveLegacyRuleUsageMetadata(context.Context, string, string) (LegacyRuleMetadata, error) {
	provider.calls++
	return LegacyRuleMetadata{}, nil
}

type legacyReplayRepositoryStub struct {
	*stubRepository
	result IngestResult
	calls  int
}

func (repository *legacyReplayRepositoryStub) ReplayLegacy(context.Context, Report) (IngestResult, bool, error) {
	repository.calls++
	return repository.result, true, nil
}

type fixedLegacyRuleProvider struct {
	metadata LegacyRuleMetadata
	nodeID   string
	ruleID   string
}

func (provider *fixedLegacyRuleProvider) ResolveLegacyRuleUsageMetadata(_ context.Context, nodeID, ruleID string) (LegacyRuleMetadata, error) {
	provider.nodeID, provider.ruleID = nodeID, ruleID
	return provider.metadata, nil
}

func completeLegacyRuleInput(t *testing.T) deploymentreceipts.Input {
	t.Helper()
	const generation int64 = 2
	const attemptID = "0123456789abcdef0123456789abcdef"
	rule := forwarding.Rule{
		ID: "rule-1", CustomerID: "customer-1", EntryGroupID: "entry-1", ExitGroupID: "exit-1",
		IngressProtocol: forwarding.IngressVLESSReality, Protocol: forwarding.ProtocolTCP,
		RealityServerName: "example.com", RealityPublicKey: "public-key", RealityShortID: "0123456789abcdef",
		Status: forwarding.StatusActive, IngressStatus: forwarding.IngressReady, Revision: 1,
	}
	input := deploymentreceipts.Input{
		Rule: rule, Node: nodes.Node{ID: "node-1", DesiredGeneration: generation, AppliedGeneration: generation},
		ActiveMember: true, CurrentAttemptID: attemptID,
	}
	setLegacyTestBundle(t, &input, `{"inbounds":[{"tag":"vless-reality-fwd_rule-1"}]}`)
	input.ApplyResults = map[agentv1.ApplyPhase]generations.ApplyResult{
		agentv1.ApplyPhaseCommit: {
			NodeID: input.Node.ID, Generation: generation, ConfigSHA256: input.CurrentConfig.ConfigSHA256,
			AttemptID: attemptID, Phase: agentv1.ApplyPhaseCommit, Status: agentv1.ApplyStatusSucceeded, EngineMode: "xray",
		},
		agentv1.ApplyPhaseVerify: {
			NodeID: input.Node.ID, Generation: generation, ConfigSHA256: input.CurrentConfig.ConfigSHA256,
			AttemptID: attemptID, Phase: agentv1.ApplyPhaseVerify, Status: agentv1.ApplyStatusSucceeded, EngineMode: "xray",
		},
	}
	return input
}

func completeLegacyGOSTRuleInput(t *testing.T, fragmentConfig string) deploymentreceipts.Input {
	t.Helper()
	input := completeLegacyRuleInput(t)
	input.Rule.IngressProtocol = forwarding.IngressTCP
	setLegacyTestBundleWithEngine(t, &input, agentv1.EngineGOST, fragmentConfig)
	for phase, result := range input.ApplyResults {
		result.EngineMode = string(agentv1.EngineGOST)
		result.ConfigSHA256 = input.CurrentConfig.ConfigSHA256
		input.ApplyResults[phase] = result
	}
	return input
}

func setLegacyTestBundle(t *testing.T, input *deploymentreceipts.Input, fragmentConfig string) {
	setLegacyTestBundleWithEngine(t, input, agentv1.EngineXray, fragmentConfig)
}

func setLegacyTestBundleWithEngine(t *testing.T, input *deploymentreceipts.Input, engine agentv1.Engine, fragmentConfig string) {
	t.Helper()
	config := json.RawMessage(fragmentConfig)
	bundle, err := json.Marshal(agentv1.ConfigurationBundle{SchemaVersion: 1, Fragments: []agentv1.ConfigurationFragment{{
		GroupID:       generations.ForwardingFragmentID(engine, input.Rule.ID),
		GroupRevision: agentv1.GroupRevision(input.Rule.Revision), Engine: engine, Config: config,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	input.CurrentConfig = &generations.NodeConfigGeneration{
		NodeID: input.Node.ID, Generation: input.Node.DesiredGeneration, Engine: agentv1.EngineNodeBundle,
		Config: bundle, ConfigSHA256: generations.SHA256Hex(bundle), CreatedAt: time.Now().UTC(),
	}
}
