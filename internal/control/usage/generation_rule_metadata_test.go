package usage

import (
	"context"
	"encoding/json"
	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	"testing"
	"time"
)

type historicalUsageRepository struct {
	legacyRuleInputRepository
	historical deploymentreceipts.HistoricalInput
}

func (r historicalUsageRepository) UsageConfigInput(context.Context, string, int64) (deploymentreceipts.HistoricalInput, error) {
	return r.historical, nil
}

func historicalUsageFixture(t *testing.T) historicalUsageRepository {
	input := completeLegacyGOSTRuleInput(t, `{"services":[{"name":"forward-rule-1","handler":{"type":"tcp"},"listener":{"type":"tcp"},"metadata":{"enableStats":true}}]}`)
	var bundle agentv1.ConfigurationBundle
	if err := json.Unmarshal(input.CurrentConfig.Config, &bundle); err != nil {
		t.Fatal(err)
	}
	bundle.Fragments[0].Usage = &agentv1.UsageMetadata{RuleID: "rule-1", CustomerID: "customer-1", EntryGroupID: "entry-1", ExitGroupID: "exit-1", Protocol: "tcp", EntryMultiplierMicros: 2_000_000, ExitMultiplierMicros: 1_000_000}
	input.CurrentConfig.Config, _ = json.Marshal(bundle)
	input.CurrentConfig.ConfigSHA256 = generations.SHA256Hex(input.CurrentConfig.Config)
	input.CurrentConfig.CreatedAt = time.Now().UTC().Add(-time.Hour)
	for phase, receipt := range input.ApplyResults {
		receipt.ConfigSHA256 = input.CurrentConfig.ConfigSHA256
		input.ApplyResults[phase] = receipt
	}
	historical := deploymentreceipts.HistoricalInput{Config: *input.CurrentConfig, AttemptID: input.CurrentAttemptID, ApplyResults: input.ApplyResults}
	// Current rule is now paused, has a different revision and no current
	// fragment. Only immutable historical accounting evidence may resolve it.
	input.Rule.Paused = true
	input.Rule.Revision++
	input.Node.DesiredGeneration++
	input.CurrentConfig = nil
	input.ActiveMember = false
	return historicalUsageRepository{legacyRuleInputRepository: legacyRuleInputRepository{input: input}, historical: historical}
}

func TestDelayedUsageSurvivesPauseAndUsesHistoricalBillingSnapshot(t *testing.T) {
	repo := historicalUsageFixture(t)
	provider := NewLegacyRuleMetadataProvider(repo)
	if _, err := provider.ResolveLegacyRuleUsageMetadata(context.Background(), "node-1", "rule-1"); err == nil {
		t.Fatal("paused rule claimed as currently deployed")
	}
	s := NewService(&stubRepository{}, stubPolicies{policy: CustomerPolicy{CustomerID: "customer-1"}}, nil, WithLegacyRuleMetadataProvider(provider))
	now := time.Now().UTC()
	report := Report{NodeID: "node-1", BootID: "boot", Sequence: 1, ConfigGeneration: agentv1.NodeConfigGeneration(repo.historical.Config.Generation), LegacyRuleID: "rule-1", RuleID: "rule-1", OccurredAt: now, PeriodStartedAt: now.Add(-time.Minute), PeriodEndedAt: now, RuleActualBytes: 42}
	got, err := s.Ingest(context.Background(), report)
	if err != nil || got.Event.ChargedBytes != 84 || got.Event.CustomerID != "customer-1" || got.Event.ConfigGeneration != 0 {
		t.Fatalf("delayed accounting: %v %v", got, err)
	}
	if !RuleReplayMatches(got.Event, report) {
		t.Fatal("historical report cannot replay its immutable ledger payload")
	}
	report.LegacyRuleID = ""
	report.CustomerID, report.EntryGroupID, report.ExitGroupID, report.Protocol = "customer-1", "entry-1", "exit-1", "tcp"
	report.CustomerActualBytes = 42
	got, err = s.Ingest(context.Background(), report)
	if err != nil || got.Event.ChargedBytes != 84 {
		t.Fatalf("full-format delayed accounting: %v %v", got, err)
	}
	report.CustomerID = "forged"
	if _, err := s.Ingest(context.Background(), report); err == nil {
		t.Fatal("forged ownership accepted")
	}
}

func TestHistoricalUsageRequiresAuthenticatedGenerationAndRealEngineReceipts(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*deploymentreceipts.HistoricalInput)
	}{
		{"other node", func(h *deploymentreceipts.HistoricalInput) { h.Config.NodeID = "other-node" }},
		{"different generation", func(h *deploymentreceipts.HistoricalInput) { h.Config.Generation++ }},
		{"tampered bundle", func(h *deploymentreceipts.HistoricalInput) { h.Config.ConfigSHA256 = "invalid" }},
		{"future bundle", func(h *deploymentreceipts.HistoricalInput) { h.Config.CreatedAt = time.Now().Add(time.Hour) }},
		{"invalidated attempt", func(h *deploymentreceipts.HistoricalInput) { h.Invalidated = true }},
		{"missing verify", func(h *deploymentreceipts.HistoricalInput) { delete(h.ApplyResults, agentv1.ApplyPhaseVerify) }},
		{"different attempt", func(h *deploymentreceipts.HistoricalInput) {
			r := h.ApplyResults[agentv1.ApplyPhaseCommit]
			r.AttemptID = "other"
			h.ApplyResults[r.Phase] = r
		}},
		{"dry run", func(h *deploymentreceipts.HistoricalInput) {
			r := h.ApplyResults[agentv1.ApplyPhaseVerify]
			r.EngineMode = "dry_run"
			h.ApplyResults[r.Phase] = r
		}},
		{"failed verify", func(h *deploymentreceipts.HistoricalInput) {
			r := h.ApplyResults[agentv1.ApplyPhaseVerify]
			r.Status = agentv1.ApplyStatusFailed
			h.ApplyResults[r.Phase] = r
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := historicalUsageFixture(t)
			generation := agentv1.NodeConfigGeneration(repo.historical.Config.Generation)
			tt.mutate(&repo.historical)
			p := NewLegacyRuleMetadataProvider(repo).(GenerationRuleMetadataProvider)
			if _, err := p.ResolveRuleUsageMetadataAt(context.Background(), "node-1", "rule-1", generation, time.Now().UTC()); err == nil {
				t.Fatal("invalid historical usage accepted")
			}
		})
	}
}
