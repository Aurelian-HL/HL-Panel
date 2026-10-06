package memoryrepo

import (
	"context"
	"encoding/json"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	"testing"
)

func TestTCPActivationUsesEveryMembersCurrentVerifiedReceipt(t *testing.T) {
	s := New(auth.Administrator{})
	rule := forwarding.Rule{ID: "rule", CustomerID: "customer", EntryGroupID: "entry", Protocol: forwarding.ProtocolTCP, Revision: 1}
	s.forwardRules[rule.ID] = rule
	s.membersByGroup["entry"] = map[string]groups.Member{}
	for _, id := range []string{"node-1", "node-2"} {
		s.nodes[id] = nodes.Node{ID: id, DesiredGeneration: 1, AppliedGeneration: 1}
		s.membersByGroup["entry"][id] = groups.Member{NodeID: id, GroupID: "entry"}
		bundle, _ := json.Marshal(agentv1.ConfigurationBundle{SchemaVersion: 1, Fragments: []agentv1.ConfigurationFragment{{GroupID: generations.ForwardingFragmentID(agentv1.EngineGOST, rule.ID), GroupRevision: 1, Engine: agentv1.EngineGOST, Config: json.RawMessage(`{"services":[]}`)}}})
		config := generations.NodeConfigGeneration{NodeID: id, Generation: 1, Engine: agentv1.EngineNodeBundle, Config: bundle, ConfigSHA256: generations.SHA256Hex(bundle)}
		s.nodeConfigsByNode[id] = map[int64]generations.NodeConfigGeneration{1: config}
		s.applyAttemptsByNode[id] = map[int64]generations.ApplyAttemptState{1: {CurrentID: "attempt"}}
		results := map[string]generations.ApplyResult{}
		for _, phase := range []agentv1.ApplyPhase{agentv1.ApplyPhaseCommit, agentv1.ApplyPhaseVerify} {
			results[string(phase)] = generations.ApplyResult{NodeID: id, Generation: 1, AttemptID: "attempt", Phase: phase, Status: agentv1.ApplyStatusSucceeded, ConfigSHA256: config.ConfigSHA256, EngineMode: "gost"}
		}
		s.applyResultsByNode[id] = map[int64]map[string]generations.ApplyResult{1: results}
	}
	assert := func(active bool) {
		t.Helper()
		got, err := s.ForwardingRule(context.Background(), "rule")
		if err != nil || got.Deployed != active {
			t.Fatalf("deployed=%v want=%v error=%v", got.Deployed, active, err)
		}
		if active && got.Status != forwarding.StatusActive {
			t.Fatal(got.Status)
		}
	}
	assert(true)
	delete(s.applyResultsByNode["node-2"][1], "verify")
	assert(false)
	member := s.membersByGroup["entry"]["node-2"]
	member.RetiredAt = &rule.CreatedAt
	s.membersByGroup["entry"]["node-2"] = member
	assert(true)
	node := s.nodes["node-1"]
	node.DesiredGeneration = 2
	s.nodes["node-1"] = node
	assert(false)
}
