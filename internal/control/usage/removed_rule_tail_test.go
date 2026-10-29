package usage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type removedRuleHistory struct {
	legacyRuleInputRepository
	current, previous deploymentreceipts.HistoricalInput
	lookups           int
	err               error
}

func (r *removedRuleHistory) UsageConfigInput(_ context.Context, nodeID string, generation int64) (deploymentreceipts.HistoricalInput, error) {
	if nodeID != "node-1" || generation != r.current.Config.Generation {
		return deploymentreceipts.HistoricalInput{}, faults.ErrNotFound
	}
	return r.current, nil
}

func (r *removedRuleHistory) UsageRuleConfigInput(_ context.Context, nodeID, ruleID string, before int64) (deploymentreceipts.HistoricalInput, error) {
	r.lookups++
	if nodeID != "node-1" || ruleID != "rule-1" || before != r.current.Config.Generation {
		return deploymentreceipts.HistoricalInput{}, faults.ErrNotFound
	}
	return r.previous, r.err
}

func replaceTailBundle(t *testing.T, h *deploymentreceipts.HistoricalInput, bundle agentv1.ConfigurationBundle, mode string) {
	t.Helper()
	h.Config.Config, _ = json.Marshal(bundle)
	h.Config.ConfigSHA256 = generations.SHA256Hex(h.Config.Config)
	for phase, receipt := range h.ApplyResults {
		receipt.ConfigSHA256, receipt.Generation, receipt.EngineMode = h.Config.ConfigSHA256, h.Config.Generation, mode
		h.ApplyResults[phase] = receipt
	}
}

func removedRuleFixture(t *testing.T, empty bool) *removedRuleHistory {
	t.Helper()
	f := historicalUsageFixture(t)
	previous := f.historical
	// Use independent maps: a broken receipt on one side must not alter the other.
	current := historicalUsageFixture(t).historical
	current.Config.Generation = previous.Config.Generation + 3
	current.Config.CreatedAt = previous.Config.CreatedAt.Add(time.Minute)
	var bundle agentv1.ConfigurationBundle
	if err := json.Unmarshal(current.Config.Config, &bundle); err != nil {
		t.Fatal(err)
	}
	if empty {
		bundle.Fragments = nil
	} else {
		bundle.Fragments[0].GroupID = generations.ForwardingFragmentID(agentv1.EngineGOST, "rule-2")
		bundle.Fragments[0].Usage.RuleID = "rule-2"
		bundle.Fragments[0].Config = json.RawMessage(`{"services":[{"name":"forward-rule-2","handler":{"type":"tcp"},"listener":{"type":"tcp"},"metadata":{"enableStats":true}}]}`)
	}
	mode := "gost"
	replaceTailBundle(t, &current, bundle, mode)
	return &removedRuleHistory{legacyRuleInputRepository: f.legacyRuleInputRepository, current: current, previous: previous}
}

func TestEmptyRuleTransitionRejectsUnattestedEngine(t *testing.T) {
	for _, mode := range []string{"", "dry_run", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			r := removedRuleFixture(t, true)
			for phase, receipt := range r.current.ApplyResults {
				receipt.EngineMode = mode
				r.current.ApplyResults[phase] = receipt
			}
			p := NewLegacyRuleMetadataProvider(r).(GenerationRuleMetadataProvider)
			if _, err := p.ResolveRuleUsageMetadataAt(context.Background(), "node-1", "rule-1", agentv1.NodeConfigGeneration(r.current.Config.Generation), time.Now().UTC()); err == nil || r.lookups != 0 {
				t.Fatal("empty or dry-run engine revived historical accounting", err)
			}
		})
	}
}

func TestRemovedRuleTailUsesLastVerifiedOwnershipAndAllowsFollowingRule(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "other rules remain", true: "last rule removed"}[empty], func(t *testing.T) {
			repo := removedRuleFixture(t, empty)
			ledger := &stubRepository{}
			s := NewService(ledger, stubPolicies{policy: CustomerPolicy{CustomerID: "customer-1"}}, nil,
				WithLegacyRuleMetadataProvider(NewLegacyRuleMetadataProvider(repo)))
			now := time.Now().UTC()
			r := Report{NodeID: "node-1", BootID: "boot", Sequence: 1, ConfigGeneration: agentv1.NodeConfigGeneration(repo.current.Config.Generation),
				LegacyRuleID: "rule-1", RuleID: "rule-1", OccurredAt: now, PeriodStartedAt: now.Add(-time.Minute), PeriodEndedAt: now, RuleActualBytes: 147480}
			got, err := s.Ingest(context.Background(), r)
			if err != nil || got.Event.CustomerID != "customer-1" || got.Event.ChargedBytes != 294960 || repo.lookups != 1 {
				t.Fatalf("removed-rule tail blocked: result=%+v error=%v lookups=%d", got, err, repo.lookups)
			}
			if _, err := NewLegacyRuleMetadataProvider(repo).ResolveLegacyRuleUsageMetadata(context.Background(), "node-1", "rule-1"); err == nil {
				t.Fatal("history revived a paused rule")
			}
			if !empty {
				r.Sequence, r.RuleID, r.LegacyRuleID, r.RuleActualBytes = 2, "rule-2", "rule-2", 1025
				got, err = s.Ingest(context.Background(), r)
				if err != nil || got.Event.RuleID != "rule-2" || got.Event.RuleActualBytes != 1025 || len(ledger.events) != 2 || repo.lookups != 1 {
					t.Fatal("following rule did not reach ledger", err)
				}
			}
		})
	}
}

func TestRemovedXrayRuleTailSupportsLegacyAndEncodedCounterTags(t *testing.T) {
	for _, encoded := range []bool{false, true} {
		for _, empty := range []bool{false, true} {
			repo := removedRuleFixture(t, empty)
			for _, input := range []*deploymentreceipts.HistoricalInput{&repo.previous, &repo.current} {
				var bundle agentv1.ConfigurationBundle
				if err := json.Unmarshal(input.Config.Config, &bundle); err != nil {
					t.Fatal(err)
				}
				for i := range bundle.Fragments {
					fragment := &bundle.Fragments[i]
					fragment.Engine = agentv1.EngineXray
					fragment.GroupID = generations.ForwardingFragmentID(agentv1.EngineXray, fragment.Usage.RuleID)
					tag := "vless-reality-fwd_" + fragment.Usage.RuleID
					if encoded {
						raw, _ := json.Marshal(fragment.Usage)
						tag = "vless-reality-" + fragment.Usage.RuleID + "--" + base64.RawURLEncoding.EncodeToString(raw)
					}
					fragment.Config, _ = json.Marshal(map[string]any{"inbounds": []map[string]string{{"tag": tag}}})
				}
				replaceTailBundle(t, input, bundle, "xray")
			}
			ledger := &stubRepository{}
			s := NewService(ledger, stubPolicies{err: faults.ErrNotFound}, nil,
				WithLegacyRuleMetadataProvider(NewLegacyRuleMetadataProvider(repo)))
			now := time.Now().UTC()
			r := Report{NodeID: "node-1", BootID: "boot", Sequence: 1, ConfigGeneration: agentv1.NodeConfigGeneration(repo.current.Config.Generation),
				LegacyRuleID: "rule-1", RuleID: "rule-1", OccurredAt: now, PeriodStartedAt: now.Add(-time.Minute), PeriodEndedAt: now, RuleActualBytes: 147480}
			if got, err := s.Ingest(context.Background(), r); err != nil || got.Event.ChargedBytes != 294960 {
				t.Fatalf("Xray tail rejected encoded=%v empty=%v: %v", encoded, empty, err)
			}
			if !empty {
				r.Sequence, r.RuleID, r.LegacyRuleID, r.RuleActualBytes = 2, "rule-2", "rule-2", 1025
				if got, err := s.Ingest(context.Background(), r); err != nil || got.Event.RuleActualBytes != 1025 || len(ledger.events) != 2 {
					t.Fatal("Xray following report blocked or rounded", err)
				}
			}
		}
	}
}

func TestRemovedRuleTailRejectsUnverifiedTransitionsAndHistoricalConflicts(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*removedRuleHistory)
	}{
		{"new missing verify", func(r *removedRuleHistory) { delete(r.current.ApplyResults, agentv1.ApplyPhaseVerify) }},
		{"new invalidated", func(r *removedRuleHistory) { r.current.Invalidated = true }},
		{"new hash mismatch", func(r *removedRuleHistory) { r.current.Config.ConfigSHA256 = "bad" }},
		{"new dry run", func(r *removedRuleHistory) {
			x := r.current.ApplyResults[agentv1.ApplyPhaseVerify]
			x.EngineMode = "dry_run"
			r.current.ApplyResults[x.Phase] = x
		}},
		{"new wrong attempt", func(r *removedRuleHistory) { r.current.AttemptID = "other" }},
		{"new future bundle", func(r *removedRuleHistory) { r.current.Config.CreatedAt = time.Now().Add(time.Hour) }},
		{"old missing verify", func(r *removedRuleHistory) { delete(r.previous.ApplyResults, agentv1.ApplyPhaseVerify) }},
		{"old invalidated", func(r *removedRuleHistory) { r.previous.Invalidated = true }},
		{"old hash mismatch", func(r *removedRuleHistory) { r.previous.Config.ConfigSHA256 = "bad" }},
		{"old wrong attempt", func(r *removedRuleHistory) { r.previous.AttemptID = "other" }},
		{"old dry run", func(r *removedRuleHistory) {
			x := r.previous.ApplyResults[agentv1.ApplyPhaseCommit]
			x.EngineMode = "dry_run"
			r.previous.ApplyResults[x.Phase] = x
		}},
		{"other node history", func(r *removedRuleHistory) { r.previous.Config.NodeID = "other-node" }},
		{"history not earlier", func(r *removedRuleHistory) { r.previous.Config.Generation = r.current.Config.Generation }},
		{"history created later", func(r *removedRuleHistory) { r.previous.Config.CreatedAt = r.current.Config.CreatedAt.Add(time.Second) }},
		{"no historical match", func(r *removedRuleHistory) { r.err = faults.ErrNotFound }},
		{"history outage", func(r *removedRuleHistory) { r.err = errors.New("isolated history outage") }},
		{"old counter absent", func(r *removedRuleHistory) {
			var b agentv1.ConfigurationBundle
			_ = json.Unmarshal(r.previous.Config.Config, &b)
			b.Fragments[0].Config = json.RawMessage(`{"services":[]}`)
			replaceTailBundle(t, &r.previous, b, "gost")
		}},
		{"old metadata absent", func(r *removedRuleHistory) {
			var b agentv1.ConfigurationBundle
			_ = json.Unmarshal(r.previous.Config.Config, &b)
			b.Fragments[0].Usage = nil
			replaceTailBundle(t, &r.previous, b, "gost")
		}},
		{"old duplicate rule", func(r *removedRuleHistory) {
			var b agentv1.ConfigurationBundle
			_ = json.Unmarshal(r.previous.Config.Config, &b)
			b.Fragments = append(b.Fragments, b.Fragments[0])
			replaceTailBundle(t, &r.previous, b, "gost")
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := removedRuleFixture(t, false)
			tt.mutate(r)
			p := NewLegacyRuleMetadataProvider(r).(GenerationRuleMetadataProvider)
			if _, err := p.ResolveRuleUsageMetadataAt(context.Background(), "node-1", "rule-1", agentv1.NodeConfigGeneration(r.current.Config.Generation), time.Now().UTC()); err == nil {
				t.Fatal("invalid tail accounting accepted")
			}
		})
	}
}

func TestPresentRuleNeverFallsBackToOlderMetadata(t *testing.T) {
	r := removedRuleFixture(t, false)
	var b agentv1.ConfigurationBundle
	_ = json.Unmarshal(r.previous.Config.Config, &b)
	b.Fragments[0].Usage = nil
	replaceTailBundle(t, &r.current, b, "gost")
	p := NewLegacyRuleMetadataProvider(r).(GenerationRuleMetadataProvider)
	if _, err := p.ResolveRuleUsageMetadataAt(context.Background(), "node-1", "rule-1", agentv1.NodeConfigGeneration(r.current.Config.Generation), time.Now().UTC()); err == nil || r.lookups != 0 {
		t.Fatal("present invalid fragment fell back to history", err, r.lookups)
	}
}

func TestRemovedRuleTailFullFormatStillChecksOwnership(t *testing.T) {
	r := removedRuleFixture(t, false)
	s := NewService(&stubRepository{}, stubPolicies{policy: CustomerPolicy{CustomerID: "customer-1"}}, nil, WithLegacyRuleMetadataProvider(NewLegacyRuleMetadataProvider(r)))
	now := time.Now().UTC()
	report := Report{NodeID: "node-1", BootID: "boot", Sequence: 1, ConfigGeneration: agentv1.NodeConfigGeneration(r.current.Config.Generation), RuleID: "rule-1", CustomerID: "forged", EntryGroupID: "entry-1", ExitGroupID: "exit-1", Protocol: "tcp", OccurredAt: now, PeriodStartedAt: now.Add(-time.Minute), PeriodEndedAt: now, RuleActualBytes: 1025, CustomerActualBytes: 1025}
	if _, err := s.Ingest(context.Background(), report); err == nil {
		t.Fatal("forged tail ownership accepted")
	}
	report.CustomerID = "customer-1"
	got, err := s.Ingest(context.Background(), report)
	if err != nil || got.Event.RuleActualBytes != 1025 || got.Event.ChargedBytes != 2050 {
		t.Fatal("full-format tail rejected or rounded", got, err)
	}
}
