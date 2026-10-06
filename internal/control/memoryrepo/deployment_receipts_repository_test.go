package memoryrepo

import (
	"context"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestRuleNodeDeploymentInputUsesCurrentGenerationAndCopiesBundle(t *testing.T) {
	store := New(auth.Administrator{})
	store.forwardRules["rule-one"] = forwarding.Rule{ID: "rule-one", CustomerID: "customer-one", EntryGroupID: "group-one",
		Revision: 2, Protocol: forwarding.ProtocolTCP, Status: forwarding.StatusPendingActivation}
	store.nodes["node-one"] = nodes.Node{ID: "node-one", DesiredGeneration: 2, AppliedGeneration: 1}
	store.membersByGroup["group-one"] = map[string]groups.Member{
		"node-one": {GroupID: "group-one", NodeID: "node-one"},
	}
	oldConfig := generations.NodeConfigGeneration{NodeID: "node-one", Generation: 1, Config: []byte(`{"schema_version":1}`)}
	currentConfig := generations.NodeConfigGeneration{NodeID: "node-one", Generation: 2, Config: []byte(`{"schema_version":1}`)}
	store.nodeConfigsByNode["node-one"] = map[int64]generations.NodeConfigGeneration{1: oldConfig, 2: currentConfig}
	store.applyResultsByNode["node-one"] = map[int64]map[string]generations.ApplyResult{
		1: {"verify": {NodeID: "node-one", Generation: 1, Phase: agentv1.ApplyPhaseVerify, Status: agentv1.ApplyStatusSucceeded}},
		2: {"commit": {NodeID: "node-one", Generation: 2, Phase: agentv1.ApplyPhaseCommit, Status: agentv1.ApplyStatusSucceeded}},
	}
	input, err := store.RuleNodeDeploymentInput(context.Background(), "rule-one", "node-one")
	if err != nil || !input.ActiveMember || input.CurrentConfig == nil || input.CurrentConfig.Generation != 2 ||
		len(input.ApplyResults) != 1 || input.ApplyResults[agentv1.ApplyPhaseCommit].Generation != 2 {
		t.Fatalf("snapshot = %#v; error = %v", input, err)
	}
	input.CurrentConfig.Config[0] = 'x'
	if store.nodeConfigsByNode["node-one"][2].Config[0] != '{' {
		t.Fatal("read-only deployment snapshot mutated stored bundle")
	}
	now := time.Now()
	member := store.membersByGroup["group-one"]["node-one"]
	member.RetiredAt = &now
	store.membersByGroup["group-one"]["node-one"] = member
	retired, err := store.RuleNodeDeploymentInput(context.Background(), "rule-one", "node-one")
	if err != nil || retired.ActiveMember {
		t.Fatalf("retired membership = %#v; error = %v", retired, err)
	}
}
