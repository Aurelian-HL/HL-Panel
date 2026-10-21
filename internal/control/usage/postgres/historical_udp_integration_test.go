package postgres

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/forwarding/gostconfig"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/usage"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type historicalUDPInput struct {
	history     deploymentreceipts.HistoricalInput
	unavailable bool
}

func (*historicalUDPInput) RuleNodeDeploymentInput(context.Context, string, string) (deploymentreceipts.Input, error) {
	return deploymentreceipts.Input{}, faults.ErrNotFound
}

func (p *historicalUDPInput) UsageConfigInput(_ context.Context, node string, generation int64) (deploymentreceipts.HistoricalInput, error) {
	if p.unavailable || node != p.history.Config.NodeID || generation != p.history.Config.Generation {
		return deploymentreceipts.HistoricalInput{}, faults.ErrNotFound
	}
	return p.history, nil
}

func TestPostgreSQLHistoricalUDPQueuePersistsAndReplays(t *testing.T) {
	for _, engine := range []agentv1.Engine{agentv1.EngineGOST, agentv1.EngineXray} {
		for _, legacy := range []bool{true, false} {
			name := string(engine) + "/managed"
			if legacy {
				name = string(engine) + "/legacy"
			}
			t.Run(name, func(t *testing.T) {
				db := isolatedUsageDatabase(t)
				metadata := &agentv1.UsageMetadata{RuleID: "rule-one", CustomerID: "customer-one", EntryGroupID: "entry-one", ExitGroupID: "exit-one", Protocol: "udp", EntryMultiplierMicros: 1500000, ExitMultiplierMicros: usage.MultiplierScale}
				var native []byte
				var err error
				if engine == agentv1.EngineGOST {
					native, err = gostconfig.CompileDirectUDP([]forwarding.Rule{{ID: metadata.RuleID, Name: "isolated", CustomerID: metadata.CustomerID, EntryGroupID: metadata.EntryGroupID,
						IngressProtocol: forwarding.IngressUDP, Protocol: forwarding.ProtocolUDP, EgressMode: forwarding.EgressDirect, ListenPort: 23001,
						Targets: []forwarding.Target{{Host: "127.0.0.1", Port: 23002}}, SelectionPolicy: forwarding.SelectionRoundRobin}}, "127.0.0.1")
				} else {
					encoded, _ := json.Marshal(metadata)
					native, err = json.Marshal(map[string]any{"inbounds": []map[string]string{{"tag": "vless-reality-kl-native-rule-one--" + base64.RawURLEncoding.EncodeToString(encoded)}}})
				}
				if err != nil {
					t.Fatal(err)
				}
				bundle, err := json.Marshal(agentv1.ConfigurationBundle{SchemaVersion: 1, Fragments: []agentv1.ConfigurationFragment{{Engine: engine, GroupID: generations.ForwardingFragmentID(engine, metadata.RuleID), GroupRevision: 1, Config: native, Usage: metadata}}})
				if err != nil {
					t.Fatal(err)
				}
				h := deploymentreceipts.HistoricalInput{Config: generations.NodeConfigGeneration{NodeID: "node-one", Generation: 7, Engine: agentv1.EngineNodeBundle, Config: bundle, ConfigSHA256: generations.SHA256Hex(bundle), CreatedAt: time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)}, AttemptID: "0123456789abcdef0123456789abcdef", ApplyResults: map[agentv1.ApplyPhase]generations.ApplyResult{}}
				for _, phase := range []agentv1.ApplyPhase{agentv1.ApplyPhaseCommit, agentv1.ApplyPhaseVerify} {
					h.ApplyResults[phase] = generations.ApplyResult{NodeID: h.Config.NodeID, Generation: h.Config.Generation, ConfigSHA256: h.Config.ConfigSHA256, AttemptID: h.AttemptID, Phase: phase, Status: agentv1.ApplyStatusSucceeded, EngineMode: string(engine)}
				}
				input := &historicalUDPInput{history: h}
				provider := usage.NewLegacyRuleMetadataProvider(input)
				// Deleted customers and paused rules still owe their immutable historical usage.
				service := usage.NewService(New(db), integrationPolicies{err: faults.ErrNotFound}, nil, usage.WithLegacyRuleMetadataProvider(provider))
				reports := make([]usage.Report, 64)
				for i := range reports {
					r := integrationReport(int64(i + 1))
					r.ConfigGeneration, r.Protocol = 7, "udp"
					r.RuleActualBytes, r.CustomerActualBytes = 101, 101
					r.PeriodEndedAt = r.PeriodEndedAt.Add(123456789 * time.Nanosecond)
					r.OccurredAt = r.PeriodEndedAt
					if legacy {
						r.LegacyRuleID = r.RuleID
						r.CustomerID, r.EntryGroupID, r.ExitGroupID, r.Protocol = "", "", "", ""
					}
					reports[i] = r
					got, err := service.Ingest(context.Background(), r)
					if err != nil || got.Replayed || got.Event.Protocol != "udp" || got.Event.CustomerID != metadata.CustomerID || got.Event.ChargedBytes != 152 || got.CustomerTotals.ChargedBytes != int64(i+1)*152 || got.Decision != nil {
						t.Fatalf("historical sequence %d failed: %v", i+1, err)
					}
				}
				// A new service must replay from durable PostgreSQL data even when history is unavailable.
				input.unavailable = true
				service = usage.NewService(New(db), integrationPolicies{err: faults.ErrNotFound}, nil, usage.WithLegacyRuleMetadataProvider(provider))
				for _, r := range reports {
					got, err := service.Ingest(context.Background(), r)
					if err != nil || !got.Replayed || got.CustomerTotals.ChargedBytes != 64*152 {
						t.Fatal("durable UDP replay failed", err)
					}
				}
				forged := reports[0]
				forged.RuleActualBytes++
				if _, err := service.Ingest(context.Background(), forged); !errors.Is(err, usage.ErrIdempotencyConflict) {
					t.Fatal("changed UDP replay accepted", err)
				}
				var count int
				if err := db.QueryRow(`SELECT count(*) FROM usage_events`).Scan(&count); err != nil || count != 64 {
					t.Fatal("UDP ledger count changed", count, err)
				}
			})
		}
	}
}
