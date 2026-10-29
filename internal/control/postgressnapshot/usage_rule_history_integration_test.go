package postgressnapshot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/usage"
	usagepostgres "github.com/hongle/hl-panel/internal/control/usage/postgres"
	"github.com/hongle/hl-panel/internal/control/usagemigration"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func ruleTailHistory(t *testing.T, node string, generation int64, rule string, engines ...agentv1.Engine) memoryrepo.NodeConfigHistory {
	t.Helper()
	engine := agentv1.EngineGOST
	if len(engines) > 0 {
		engine = engines[0]
	}
	metadata := &agentv1.UsageMetadata{RuleID: rule, CustomerID: "tail-customer", EntryGroupID: "history-group",
		Protocol: "tcp", EntryMultiplierMicros: usage.MultiplierScale, ExitMultiplierMicros: usage.MultiplierScale}
	native := json.RawMessage(fmt.Sprintf(`{"services":[{"name":"forward-%s","handler":{"type":"tcp"},"listener":{"type":"tcp"},"metadata":{"enableStats":true}}]}`, rule))
	if engine == agentv1.EngineXray {
		native = json.RawMessage(fmt.Sprintf(`{"inbounds":[{"tag":"vless-reality-fwd_%s"}]}`, rule))
	}
	raw, err := json.Marshal(agentv1.ConfigurationBundle{SchemaVersion: 1, Fragments: []agentv1.ConfigurationFragment{{
		GroupID: generations.ForwardingFragmentID(engine, rule), Engine: engine,
		GroupRevision: 1, Config: native, Usage: metadata,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	attempt := "0123456789abcdef0123456789abcdef"
	h := memoryrepo.NodeConfigHistory{Config: generations.NodeConfigGeneration{
		ID: fmt.Sprintf("tail-%s-%d", node, generation), NodeID: node, Generation: generation, Engine: agentv1.EngineNodeBundle,
		Config: raw, ConfigSHA256: generations.SHA256Hex(raw), CreatedAt: time.Date(2026, 10, 27, 0, int(generation), 0, 0, time.UTC)},
		Attempt: generations.ApplyAttemptState{CurrentID: attempt, SeenIDs: map[string]bool{attempt: true}},
		Results: make(map[string]generations.ApplyResult),
	}
	for _, phase := range []agentv1.ApplyPhase{agentv1.ApplyPhaseCommit, agentv1.ApplyPhaseVerify} {
		h.Results[string(phase)] = generations.ApplyResult{ID: string(phase), NodeID: node, Generation: generation,
			ConfigSHA256: h.Config.ConfigSHA256, AttemptID: attempt, Phase: phase, Status: agentv1.ApplyStatusSucceeded, EngineMode: string(engine)}
	}
	return h
}

func seedRuleTailHistory(t *testing.T, s *Store, hot, archived []memoryrepo.NodeConfigHistory) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	state, err := readState(ctx, tx, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.RestoreNodeConfigHistory(hot); err != nil {
		t.Fatal(err)
	}
	raw, err := state.EncodeSnapshotWithAudit(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memoryrepo.DecodeSnapshot(raw); err != nil {
		t.Fatal("invalid isolated hot history fixture", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET payload=$1,revision=revision+1`, raw); err != nil {
		t.Fatal(err)
	}
	if err := persistNodeConfigHistory(ctx, tx, archived); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestUsageRuleHistorySelectsLatestAcrossHotAndArchivedEvidence(t *testing.T) {
	for _, scenario := range []string{"archive only", "hot authoritative", "newer archive", "newer hot"} {
		t.Run(scenario, func(t *testing.T) {
			s, _ := nodeHistoryFixture(t)
			old := ruleTailHistory(t, "history-node", 3, "tail-rule")
			latest := ruleTailHistory(t, "history-node", 5, "tail-rule")
			latest.Attempt.Invalidated = true
			prefix := ruleTailHistory(t, "history-node", 6, "tail-rule-extra")
			foreign := ruleTailHistory(t, "foreign-node", 7, "tail-rule")
			var hot []memoryrepo.NodeConfigHistory
			archived := []memoryrepo.NodeConfigHistory{old, latest, prefix, foreign}
			wantGeneration, wantInvalid := int64(5), true
			switch scenario {
			case "hot authoritative":
				hot = []memoryrepo.NodeConfigHistory{latest}
				archived[1].Attempt.Invalidated = false
			case "newer archive":
				hot = []memoryrepo.NodeConfigHistory{old}
			case "newer hot":
				newest := ruleTailHistory(t, "history-node", 8, "tail-rule")
				hot = []memoryrepo.NodeConfigHistory{newest}
				wantGeneration, wantInvalid = 8, false
			}
			seedRuleTailHistory(t, s, hot, archived)
			ctx := context.Background()
			got, err := s.UsageRuleConfigInput(ctx, "history-node", "tail-rule", 9)
			if err != nil || got.Config.Generation != wantGeneration || got.Invalidated != wantInvalid {
				t.Fatalf("latest history: generation=%d invalid=%v err=%v", got.Config.Generation, got.Invalidated, err)
			}
			got, err = s.UsageRuleConfigInput(ctx, "history-node", "tail-rule", 5)
			if err != nil || got.Config.Generation != 3 {
				t.Fatal("before-generation must be strict", got.Config.Generation, err)
			}
			if _, err := s.UsageRuleConfigInput(ctx, "history-node", "missing-rule", 9); !errors.Is(err, faults.ErrNotFound) {
				t.Fatal("unmatched history accepted", err)
			}
			if _, err := s.UsageRuleConfigInput(ctx, "missing-node", "tail-rule", 9); !errors.Is(err, faults.ErrNotFound) {
				t.Fatal("other node's history accepted", err)
			}
			if scenario == "archive only" {
				corrupt := latest
				corrupt.Config.ConfigSHA256 = "bad"
				raw, _ := json.Marshal(corrupt)
				if _, err := s.db.ExecContext(ctx, `UPDATE hl_panel_node_config_history SET payload=$1 WHERE node_id=$2 AND generation=$3`, raw, "history-node", 5); err != nil {
					t.Fatal(err)
				}
				if _, err := s.UsageRuleConfigInput(ctx, "history-node", "tail-rule", 9); err == nil {
					t.Fatal("corrupt latest archive was skipped for an older good record")
				}
			}
		})
	}
}

type tailUsagePolicies struct{}

func (tailUsagePolicies) UsagePolicy(context.Context, string) (usage.CustomerPolicy, error) {
	return usage.CustomerPolicy{}, faults.ErrNotFound
}

func TestRemovedRuleTailUnblocksPostgreSQLAccountingWithoutLosingBytes(t *testing.T) {
	s, _ := nodeHistoryFixture(t)
	ctx := context.Background()
	old := ruleTailHistory(t, "history-node", 13, "paused-rule", agentv1.EngineXray)
	current := ruleTailHistory(t, "history-node", 14, "new-rule", agentv1.EngineXray)
	otherOld := ruleTailHistory(t, "second-node", 1, "paused-rule", agentv1.EngineXray)
	otherCurrent := ruleTailHistory(t, "second-node", 2, "new-rule", agentv1.EngineXray)
	seedRuleTailHistory(t, s, []memoryrepo.NodeConfigHistory{current}, []memoryrepo.NodeConfigHistory{old, otherOld, otherCurrent})
	migrations, err := usagemigration.New(s.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	ledger := usagepostgres.New(s.db)
	service := usage.NewService(ledger, tailUsagePolicies{}, nil, usage.WithLegacyRuleMetadataProvider(usage.NewLegacyRuleMetadataProvider(s)))
	ended := time.Date(2026, 10, 27, 9, 2, 15, 123456789, time.UTC)
	var reports []usage.Report
	for _, node := range []struct {
		id         string
		generation agentv1.NodeConfigGeneration
		tail       int64
	}{{"history-node", 14, 147480}, {"second-node", 2, 146339}} {
		for i, count := range []int64{node.tail, 1, 1024, 1025} {
			rule := "new-rule"
			if i == 0 {
				rule = "paused-rule"
			}
			r := usage.Report{NodeID: node.id, BootID: "tail-boot", Sequence: int64(i + 1), ConfigGeneration: node.generation,
				RuleID: rule, LegacyRuleID: rule, RuleActualBytes: count, CustomerActualBytes: count,
				OccurredAt: ended, PeriodStartedAt: ended.Add(-40 * time.Second), PeriodEndedAt: ended}
			got, err := service.Ingest(ctx, r)
			if err != nil || got.Replayed || got.Event.RuleActualBytes != count || got.Event.ChargedBytes != count {
				t.Fatalf("ordered accounting blocked or rounded node=%s seq=%d: %+v %v", node.id, r.Sequence, got, err)
			}
			reports = append(reports, r)
		}
	}
	for rule, want := range map[string]int64{"paused-rule": 293819, "new-rule": 4100} {
		got, err := ledger.RuleChargedBytes(ctx, usage.Query{Scope: usage.ScopeRule, ScopeID: rule})
		if err != nil || got != want {
			t.Fatal("multi-node byte total is not exact", rule, got, want, err)
		}
	}
	// Reconstruct the service, make history unavailable, and replay durable reports.
	service = usage.NewService(usagepostgres.New(s.db), tailUsagePolicies{}, nil, usage.WithLegacyRuleMetadataProvider(usage.NewLegacyRuleMetadataProvider(memoryrepo.New(bootstrapAdministrator(t)))))
	for _, r := range reports {
		if got, err := service.Ingest(ctx, r); err != nil || !got.Replayed {
			t.Fatal("durable replay required live history", err)
		}
	}
	forged := reports[0]
	forged.RuleActualBytes++
	if _, err := service.Ingest(ctx, forged); !errors.Is(err, usage.ErrIdempotencyConflict) {
		t.Fatal("changed replay accepted", err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM usage_events`).Scan(&count); err != nil || count != 8 {
		t.Fatal("replay double counted usage", count, err)
	}
}
