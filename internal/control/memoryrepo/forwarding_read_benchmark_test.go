package memoryrepo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// Use real rule/receipt evaluation against a shared multi-rule bundle, rather
// than an empty rule list. This is synthetic and not a production latency test.
func BenchmarkForwardingRulesReadView(b *testing.B) {
	s, request := forwardingRepositoryFixture(b)
	ctx := context.Background()
	network := s.groupNetworks["entry-1"]
	network.PortEnd = 13000
	s.groupNetworks["entry-1"] = network
	s.nodes["node"] = nodes.Node{ID: "node", CredentialHash: "benchmark-node-credential", DesiredGeneration: 1, AppliedGeneration: 1, Capabilities: []string{"gost"}}
	s.nodesByCredential["benchmark-node-credential"] = "node"
	s.membersByGroup["entry-1"]["node"] = groups.Member{GroupID: "entry-1", NodeID: "node", DialHost: "node.example.test", Weight: 1}
	service := forwarding.NewService(s, nil)
	var bundle agentv1.ConfigurationBundle
	bundle.SchemaVersion = 1
	for i := 0; i < 200; i++ {
		request.Name = fmt.Sprintf("rule-%03d", i)
		rule, _, err := service.CreateForCustomerByAdministrator(ctx, "admin-test", request.CustomerID, request, request.Name)
		if err != nil {
			b.Fatal(err)
		}
		bundle.Fragments = append(bundle.Fragments, agentv1.ConfigurationFragment{
			GroupID: generations.ForwardingFragmentID(agentv1.EngineGOST, rule.ID), GroupRevision: agentv1.GroupRevision(rule.Revision),
			Engine: agentv1.EngineGOST, Config: json.RawMessage(`{"services":[],"padding":"` + strings.Repeat("x", 1024) + `"}`),
		})
	}
	rawConfig, err := json.Marshal(bundle)
	if err != nil {
		b.Fatal(err)
	}
	node := s.nodes["node"]
	node.DesiredGeneration, node.AppliedGeneration = 1, 1
	s.nodes["node"] = node
	config := generations.NodeConfigGeneration{NodeID: "node", Generation: 1, Engine: agentv1.EngineNodeBundle, Config: rawConfig, ConfigSHA256: generations.SHA256Hex(rawConfig)}
	s.nodeConfigsByNode["node"] = map[int64]generations.NodeConfigGeneration{1: config}
	const attemptID = "0123456789abcdef0123456789abcdef"
	s.applyAttemptsByNode["node"] = map[int64]generations.ApplyAttemptState{1: {CurrentID: attemptID, SeenIDs: map[string]bool{attemptID: true}}}
	s.applyResultsByNode["node"] = map[int64]map[string]generations.ApplyResult{1: {}}
	for _, phase := range []agentv1.ApplyPhase{agentv1.ApplyPhaseCommit, agentv1.ApplyPhaseVerify} {
		s.applyResultsByNode["node"][1][string(phase)] = generations.ApplyResult{NodeID: "node", Generation: 1, ConfigSHA256: config.ConfigSHA256, Phase: phase, Status: agentv1.ApplyStatusSucceeded, EngineMode: "gost", AttemptID: attemptID}
	}
	raw, err := s.EncodeSnapshotWithAudit(nil)
	if err != nil {
		b.Fatal(err)
	}
	view := s.ForwardingReadView()
	check := func(items []forwarding.Rule, err error) {
		if err != nil || len(items) != 200 {
			b.Fatalf("rule count %d: %v", len(items), err)
		}
		for _, item := range items {
			if item.Status != forwarding.StatusActive {
				input, _ := s.RuleNodeDeploymentInput(ctx, item.ID, "node")
				b.Fatalf("benchmark did not evaluate matching receipts: %s %+v", item.Status, deploymentreceipts.Evaluate(input))
			}
		}
	}
	check(view.ListForwardingRules(ctx))
	managed, err := view.ListManagedForwardingRules(ctx, "admin-test")
	if err != nil || len(managed) != 200 {
		b.Fatalf("benchmark needs 200 delegated rules, got %d: %v", len(managed), err)
	}
	target := managed[0].ID
	b.Run("authorized_list_for_one", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			items, err := view.ListManagedForwardingRules(ctx, "admin-test")
			if err != nil || len(items) != 200 || items[0].ID != target {
				b.Fatal("authorized list lost benchmark rule", err)
			}
		}
	})
	b.Run("authorized_single", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			rule, err := view.ManagedForwardingRule(ctx, "admin-test", target)
			if err != nil || rule.ID != target || rule.Status != forwarding.StatusActive {
				b.Fatal("single rule lost authorization or receipts", err)
			}
		}
	})
	b.Run("decode_and_evaluate", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			state, err := DecodeSnapshot(raw)
			if err != nil {
				b.Fatal(err)
			}
			check(state.ListForwardingRules(ctx))
		}
	})
	b.Run("overlay_and_evaluate", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			check(view.WithOverlays(3, nil, nil, nil).ListForwardingRules(ctx))
		}
	})
	b.Run("publish_projection", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = s.ForwardingReadView()
		}
	})
}
