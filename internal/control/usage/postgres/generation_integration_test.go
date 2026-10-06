package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/usage"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type generationMetadata struct{ unavailable bool }

func (*generationMetadata) ResolveLegacyRuleUsageMetadata(context.Context, string, string) (usage.LegacyRuleMetadata, error) {
	return usage.LegacyRuleMetadata{}, errors.New("current rule is paused")
}
func (p *generationMetadata) ResolveRuleUsageMetadataAt(_ context.Context, nodeID, ruleID string, generation agentv1.NodeConfigGeneration, _ time.Time) (usage.LegacyRuleMetadata, error) {
	if p.unavailable || nodeID != "node-one" || ruleID != "rule-one" || generation != 7 {
		return usage.LegacyRuleMetadata{}, errors.New("unverified generation")
	}
	return usage.LegacyRuleMetadata{RuleID: ruleID, CustomerID: "customer-one", EntryGroup: "entry-one", ExitGroup: "exit-one", Protocol: "tcp", EntryMultiplierMicros: 1500000, ExitMultiplierMicros: usage.MultiplierScale}, nil
}

func TestPostgreSQLDelayedGenerationReportPersistsAndReplaysAfterPause(t *testing.T) {
	db := isolatedUsageDatabase(t)
	provider := &generationMetadata{}
	service := usage.NewService(New(db), integrationPolicies{}, nil, usage.WithLegacyRuleMetadataProvider(provider))
	report := integrationReport(1)
	report.ConfigGeneration = 7
	report.LegacyRuleID = report.RuleID
	report.CustomerID, report.EntryGroupID, report.ExitGroupID, report.Protocol = "", "", "", ""
	report.RuleActualBytes, report.CustomerActualBytes = 101, 101
	report.PeriodEndedAt = report.PeriodEndedAt.Add(123456789 * time.Nanosecond)
	report.OccurredAt = report.PeriodEndedAt
	result, err := service.Ingest(context.Background(), report)
	if err != nil || result.Event.ChargedBytes != 152 || result.Event.ConfigGeneration != 0 {
		t.Fatalf("persist historical billing: %+v %v", result, err)
	}
	provider.unavailable = true
	// Recreate service/repository so replay depends on the PostgreSQL ledger.
	service = usage.NewService(New(db), integrationPolicies{}, nil, usage.WithLegacyRuleMetadataProvider(provider))
	replayed, err := service.Ingest(context.Background(), report)
	if err != nil || !replayed.Replayed || replayed.CustomerTotals.ChargedBytes != 152 {
		t.Fatalf("replay delayed report: %+v %v", replayed, err)
	}
	report.RuleActualBytes++
	if _, err := service.Ingest(context.Background(), report); !errors.Is(err, usage.ErrIdempotencyConflict) {
		t.Fatalf("changed report accepted: %v", err)
	}
}
