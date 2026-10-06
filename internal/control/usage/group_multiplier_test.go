package usage

import (
	"context"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"testing"
	"time"
)

func TestLegacyTrafficUsesGroupMultipliersAndIgnoresForgedAgentValues(t *testing.T) {
	for _, tt := range []struct {
		name        string
		entry, exit float64
		mode        forwarding.EgressMode
		charge      int64
	}{
		{"direct entry only", 2, 9, forwarding.EgressDirect, 84},
		{"both groups", 2, 1.5, forwarding.EgressExitGroup, 126},
		{"free traffic", 0, 1.5, forwarding.EgressExitGroup, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := completeLegacyRuleInput(t)
			input.Rule.EgressMode = tt.mode
			input.EntryNetwork = &groupconfig.GroupNetwork{TrafficMultiplier: tt.entry}
			input.ExitNetwork = &groupconfig.GroupNetwork{TrafficMultiplier: tt.exit}
			repo := &stubRepository{}
			s := NewService(repo, stubPolicies{policy: CustomerPolicy{CustomerID: "customer-1"}}, nil, WithLegacyRuleMetadataProvider(NewLegacyRuleMetadataProvider(legacyRuleInputRepository{input: input})))
			now := time.Now().UTC()
			report := Report{NodeID: "node-1", BootID: "boot", Sequence: 1, LegacyRuleID: "rule-1", OccurredAt: now, PeriodStartedAt: now.Add(-time.Minute), PeriodEndedAt: now, RuleActualBytes: 42, EntryMultiplierMicros: 999, ExitMultiplierMicros: 999}
			got, err := s.Ingest(context.Background(), report)
			if err != nil || got.Event.ChargedBytes != tt.charge || got.Event.CustomerActualBytes != 42 {
				t.Fatalf("billing=%+v error=%v", got, err)
			}
			if !LegacyReplayMatches(got.Event, report) {
				t.Fatal("retry cannot replay stored multiplier snapshot")
			}
			report.RuleActualBytes++
			if LegacyReplayMatches(got.Event, report) {
				t.Fatal("changed byte count replayed")
			}
		})
	}
}
