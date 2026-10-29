package memoryrepo

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestUsageRuleHistorySelectsLatestExactMatchWithoutSkippingBadReceipts(t *testing.T) {
	s := New(auth.Administrator{})
	for _, entry := range []struct {
		node, rule string
		generation int64
		invalid    bool
	}{
		{"node-one", "rule-one", 1, false},
		{"node-one", "rule-one", 3, true},
		{"node-one", "rule-one-suffix", 4, false},
		{"node-one", "rule-one", 5, false},
		{"node-two", "rule-one", 6, false},
	} {
		raw, err := json.Marshal(agentv1.ConfigurationBundle{SchemaVersion: 1, Fragments: []agentv1.ConfigurationFragment{{
			Engine: agentv1.EngineXray, GroupID: generations.ForwardingFragmentID(agentv1.EngineXray, entry.rule), Config: json.RawMessage(`{}`),
		}}})
		if err != nil {
			t.Fatal(err)
		}
		s.restoreNodeConfigHistoryLocked(NodeConfigHistory{
			Config: generations.NodeConfigGeneration{ID: "config", NodeID: entry.node, Generation: entry.generation,
				Engine: agentv1.EngineNodeBundle, Config: raw, ConfigSHA256: generations.SHA256Hex(raw), CreatedAt: time.Now().UTC()},
			Results: map[string]generations.ApplyResult{"commit": {Status: agentv1.ApplyStatusSucceeded}},
			Attempt: generations.ApplyAttemptState{CurrentID: "0123456789abcdef0123456789abcdef",
				SeenIDs: map[string]bool{"0123456789abcdef0123456789abcdef": true}, Invalidated: entry.invalid},
		})
	}
	view := s.UsageReadView()
	for _, repository := range []struct {
		name string
		repo deploymentreceipts.HistoricalRuleRepository
	}{{"store", s}, {"view", view}} {
		t.Run(repository.name, func(t *testing.T) {
			for _, tt := range []struct {
				node, rule   string
				before, want int64
			}{{"node-one", "rule-one", 5, 3}, {"node-one", "rule-one", 3, 1},
				{"node-one", "rule-one", 6, 5}, {"node-two", "rule-one", 7, 6}} {
				got, err := repository.repo.UsageRuleConfigInput(context.Background(), tt.node, tt.rule, tt.before)
				if err != nil || got.Config.Generation != tt.want || got.Invalidated != (tt.want == 3) {
					t.Fatalf("latest exact match before %d = %+v: %v", tt.before, got, err)
				}
				got.Config.Config[0] = 'x'
				delete(got.ApplyResults, agentv1.ApplyPhaseCommit)
				again, err := repository.repo.UsageRuleConfigInput(context.Background(), tt.node, tt.rule, tt.before)
				if err != nil || again.Config.Config[0] != '{' || len(again.ApplyResults) != 1 {
					t.Fatal("returned history changed repository evidence", err)
				}
			}
			for _, node := range []string{"node-one", "unknown-node"} {
				if _, err := repository.repo.UsageRuleConfigInput(context.Background(), node, "absent-rule", 7); !errors.Is(err, faults.ErrNotFound) {
					t.Fatal("missing history accepted", err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := repository.repo.UsageRuleConfigInput(ctx, "node-one", "rule-one", 7); !errors.Is(err, context.Canceled) {
				t.Fatal("canceled lookup was not canceled", err)
			}
		})
	}
}
