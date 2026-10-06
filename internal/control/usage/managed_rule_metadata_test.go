package usage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
)

func TestManagedVLESSTrafficUsesVerifiedOwnershipAndGroupMultipliers(t *testing.T) {
	for _, tt := range []struct {
		name        string
		entry, exit float64
		mode        forwarding.EgressMode
		charge      int64
	}{
		{"entry only", 2, 9, forwarding.EgressDirect, 84},
		{"both groups", 2, 1.5, forwarding.EgressExitGroup, 126},
		{"free traffic", 0, 1.5, forwarding.EgressExitGroup, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := completeLegacyRuleInput(t)
			input.Rule.EgressMode = tt.mode
			input.EntryNetwork = &groupconfig.GroupNetwork{TrafficMultiplier: tt.entry}
			input.ExitNetwork = &groupconfig.GroupNetwork{TrafficMultiplier: tt.exit}
			metadata := map[string]string{"rule_id": "rule-1", "customer_id": "customer-1", "entry_group_id": "entry-1", "exit_group_id": "exit-1", "protocol": "tcp"}
			raw, _ := json.Marshal(metadata)
			tag := "vless-reality-rule-1--" + base64.RawURLEncoding.EncodeToString(raw)
			fragment, _ := json.Marshal(map[string]any{"inbounds": []map[string]string{{"tag": tag}}})
			setLegacyTestBundle(t, &input, string(fragment))
			for phase, receipt := range input.ApplyResults {
				receipt.ConfigSHA256 = input.CurrentConfig.ConfigSHA256
				input.ApplyResults[phase] = receipt
			}
			repo := &stubRepository{}
			service := NewService(repo, stubPolicies{policy: CustomerPolicy{CustomerID: "customer-1"}}, nil, WithLegacyRuleMetadataProvider(NewLegacyRuleMetadataProvider(legacyRuleInputRepository{input: input})))
			now := time.Now().UTC()
			report := Report{NodeID: "node-1", BootID: "boot", Sequence: 1, RuleID: "rule-1", CustomerID: "customer-1", EntryGroupID: "entry-1", ExitGroupID: "exit-1", Protocol: "tcp", OccurredAt: now, PeriodStartedAt: now.Add(-time.Minute), PeriodEndedAt: now, RuleActualBytes: 42, CustomerActualBytes: 42, EntryMultiplierMicros: 999, ExitMultiplierMicros: 999}
			got, err := service.Ingest(context.Background(), report)
			if err != nil || got.Event.ChargedBytes != tt.charge {
				t.Fatalf("billing=%+v error=%v", got, err)
			}
			if !RuleReplayMatches(got.Event, report) {
				t.Fatal("full report cannot replay stored multiplier snapshot")
			}
			for _, mutate := range []func(*Report){func(r *Report) { r.CustomerID = "forged" }, func(r *Report) { r.RuleActualBytes++ }, func(r *Report) { r.CustomerActualBytes++ }, func(r *Report) { r.EntryGroupID = "forged" }} {
				forged := report
				mutate(&forged)
				if RuleReplayMatches(got.Event, forged) {
					t.Fatal("forged full report replayed")
				}
				if _, err := service.Ingest(context.Background(), forged); !errors.Is(err, faults.ErrValidation) {
					t.Fatalf("forged report error=%v", err)
				}
			}
			metadata["customer_id"] = "forged"
			raw, _ = json.Marshal(metadata)
			fragment, _ = json.Marshal(map[string]any{"inbounds": []map[string]string{{"tag": "vless-reality-rule-1--" + base64.RawURLEncoding.EncodeToString(raw)}}})
			setLegacyTestBundle(t, &input, string(fragment))
			for phase, receipt := range input.ApplyResults {
				receipt.ConfigSHA256 = input.CurrentConfig.ConfigSHA256
				input.ApplyResults[phase] = receipt
			}
			provider := NewLegacyRuleMetadataProvider(legacyRuleInputRepository{input: input})
			if _, err := provider.ResolveLegacyRuleUsageMetadata(context.Background(), "node-1", "rule-1"); !errors.Is(err, faults.ErrValidation) {
				t.Fatalf("forged fragment tag error=%v", err)
			}
		})
	}
}

func TestManagedReplayRunsBeforeCurrentRuleResolver(t *testing.T) {
	repository := &legacyReplayRepositoryStub{stubRepository: &stubRepository{}, result: IngestResult{Replayed: true}}
	provider := &countingLegacyRuleProvider{}
	service := NewService(repository, stubPolicies{}, nil, WithLegacyRuleMetadataProvider(provider))
	if got, err := service.Ingest(context.Background(), Report{RuleID: "rule-1"}); err != nil || !got.Replayed || provider.calls != 0 || repository.calls != 1 {
		t.Fatalf("replay=%+v resolver=%d error=%v", got, provider.calls, err)
	}
}
