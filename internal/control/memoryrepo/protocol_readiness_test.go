package memoryrepo

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// Supply a matching committed/verified bundle and bound protocol proof, rather
// than treating the old fixture's heartbeat timestamp as protocol health.
func grantFixtureProtocolHealth(t *testing.T, s *Store, ruleID, poolID, nodeID string) {
	t.Helper()
	rule := s.forwardRules[ruleID]
	node := s.nodes[nodeID]
	node.DesiredGeneration, node.AppliedGeneration = 1, 1
	s.nodes[nodeID] = node
	raw, err := json.Marshal(agentv1.ConfigurationBundle{SchemaVersion: 1, Fragments: []agentv1.ConfigurationFragment{{GroupID: generations.ForwardingFragmentID(agentv1.EngineXray, ruleID), GroupRevision: agentv1.GroupRevision(rule.Revision), Engine: agentv1.EngineXray, Config: json.RawMessage(`{"inbounds":[]}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	hash := generations.SHA256Hex(raw)
	s.nodeConfigsByNode[nodeID] = map[int64]generations.NodeConfigGeneration{1: {NodeID: nodeID, Generation: 1, Engine: agentv1.EngineNodeBundle, Config: raw, ConfigSHA256: hash}}
	const attemptID = "0123456789abcdef0123456789abcdef"
	s.applyAttemptsByNode[nodeID] = map[int64]generations.ApplyAttemptState{1: {CurrentID: attemptID, SeenIDs: map[string]bool{attemptID: true}}}
	results := make(map[string]generations.ApplyResult)
	for _, phase := range []agentv1.ApplyPhase{agentv1.ApplyPhaseCommit, agentv1.ApplyPhaseVerify} {
		results[string(phase)] = generations.ApplyResult{NodeID: nodeID, Generation: 1, ConfigSHA256: hash, Phase: phase, Status: agentv1.ApplyStatusSucceeded, EngineMode: "xray", AttemptID: attemptID}
	}
	s.applyResultsByNode[nodeID] = map[int64]map[string]generations.ApplyResult{1: results}
	pool := s.endpointPools[poolID]
	pool.RuleID = ruleID
	pool.Port = rule.ListenPort
	s.endpointPools[poolID] = pool
	member := s.endpointMembers[poolID][nodeID]
	member.DialHost = "node.example.test"
	s.endpointMembers[poolID][nodeID] = member
	now := time.Now().UTC()
	proof := gatewaymembership.ProtocolObservation{RuleID: ruleID, NodeID: nodeID, RuleRevision: rule.Revision, NodeConfigGeneration: 1, ConfigSHA256: hash, Protocol: forwarding.IngressVLESSReality, DialHost: member.DialHost, VerifiedAt: now, LeaseExpiresAt: now.Add(endpoints.DefaultHealthTTL)}
	if err := s.PublishProtocolHealth(context.Background(), proof); err != nil {
		t.Fatal(err)
	}
}

func TestRuleReadinessFollowsLeaseAndBatchActivatesOnce(t *testing.T) {
	s, proof := protocolPublicationFixture(t)
	stale := proof
	stale.RuleRevision++
	rejected, err := s.PublishProtocolHealthBatch(context.Background(), []gatewaymembership.ProtocolObservation{stale, proof})
	if err != nil || len(rejected) != 2 || rejected[0] == nil || rejected[1] != nil {
		t.Fatalf("independent batch validation: %v %v", rejected, err)
	}
	assert := func(ready bool) {
		t.Helper()
		rule, err := s.ForwardingRule(context.Background(), proof.RuleID)
		count, countErr := gatewaymembership.NewService(s).ReadyCandidateCount(context.Background(), "pool")
		if err != nil || countErr != nil || rule.Deployed != ready || (rule.Status == forwarding.StatusActive) != ready || (count > 0) != ready {
			t.Fatalf("rule and gateway disagree: ready=%v rule=%v count=%d errors=%v/%v", ready, rule.Status, count, err, countErr)
		}
	}
	assert(true)
	if _, err = s.PublishProtocolHealthBatch(context.Background(), []gatewaymembership.ProtocolObservation{proof}); err != nil {
		t.Fatal(err)
	}
	if len(s.auditEvents) != 1 {
		t.Fatal("repeat health created duplicate activation audit")
	}
	old := proof
	old.VerifiedAt = time.Now().Add(-2 * time.Minute)
	old.LeaseExpiresAt = old.VerifiedAt.Add(endpoints.DefaultHealthTTL)
	s.protocolHealth["pool"][proof.NodeID] = old
	assert(false)
	// Health expiration is a read projection, not a destructive deployment edit.
	if !s.forwardRules[proof.RuleID].Deployed {
		t.Fatal("lease expiry altered stored deployment")
	}
	if err = s.PublishProtocolHealth(context.Background(), proof); err != nil {
		t.Fatal(err)
	}
	assert(true)
	member := s.endpointMembers["pool"][proof.NodeID]
	member.DialHost = "changed.example.test"
	s.endpointMembers["pool"][proof.NodeID] = member
	assert(false)
	if err = s.MarkActivated(context.Background(), proof.RuleID, proof.RuleRevision, audit.Event{ID: "duplicate"}); err != nil {
		t.Fatal(err)
	}
	assert(false)
}

func TestRuleRefreshExplainsCurrentReadinessWithoutChangingStoredDeployment(t *testing.T) {
	cases := []struct {
		name   string
		reason forwarding.ActivationReason
		change func(*Store)
	}{
		{"healthy", "", func(*Store) {}},
		{"lease expired", "protocol_health_expired", func(s *Store) {
			proof := s.protocolHealth["pool"]["node"]
			proof.VerifiedAt = time.Now().Add(-2 * time.Minute)
			proof.LeaseExpiresAt = proof.VerifiedAt.Add(endpoints.DefaultHealthTTL)
			s.protocolHealth["pool"]["node"] = proof
		}},
		{"awaiting challenge", "protocol_verification_pending", func(s *Store) { delete(s.protocolHealth["pool"], "node") }},
		{"proof for different config", "protocol_verification_pending", func(s *Store) {
			proof := s.protocolHealth["pool"]["node"]
			proof.NodeConfigGeneration++
			s.protocolHealth["pool"]["node"] = proof
		}},
		{"awaiting apply", "node_configuration_pending", func(s *Store) {
			node := s.nodes["node"]
			node.AppliedGeneration = 0
			s.nodes["node"] = node
		}},
		{"no eligible node", "no_available_nodes", func(s *Store) {
			member := s.endpointMembers["pool"]["node"]
			member.Weight = 0
			s.endpointMembers["pool"]["node"] = member
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, proof := protocolPublicationFixture(t)
			if _, err := s.PublishProtocolHealthBatch(context.Background(), []gatewaymembership.ProtocolObservation{proof}); err != nil {
				t.Fatal(err)
			}
			tc.change(s)
			for refresh := 0; refresh < 2; refresh++ {
				rules, err := s.ListForwardingRules(context.Background())
				if err != nil || len(rules) != 1 {
					t.Fatalf("rule refresh failed: %v", err)
				}
				ready := tc.reason == ""
				if rules[0].ActivationReason != tc.reason || (rules[0].Status == forwarding.StatusActive) != ready || rules[0].Deployed != ready {
					t.Fatalf("refresh gave misleading readiness: status=%s reason=%s deployed=%v want=%s", rules[0].Status, rules[0].ActivationReason, rules[0].Deployed, tc.reason)
				}
				if !s.forwardRules["rule"].Deployed || s.forwardRules["rule"].ActivationReason != "" {
					t.Fatal("read projection altered stored deployment or authorization")
				}
				state, err := s.GatewayMembershipState(context.Background(), "pool")
				if err != nil || (gatewaymembership.ReadyCandidateCount(state, time.Now()) > 0) != ready {
					t.Fatal("display reason changed routing authorization", err)
				}
			}
		})
	}
}

func TestReadinessExplanationRemainsTransientWhenEditingProvisionedRule(t *testing.T) {
	f := newVLESSBundleFixture(t, true)
	pool := f.store.endpointPools[f.endpointID]
	pool.Port = f.rule.ListenPort
	f.store.endpointPools[f.endpointID] = pool
	ctx := context.Background()
	raw, err := json.Marshal(f.store.forwardRules[f.rule.ID])
	if err != nil {
		t.Fatal(err)
	}
	var request forwarding.Request
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	request.Name = "renamed provisioned rule"
	view, _, err := forwarding.NewService(f.store, nil).UpdateForAdministrator(ctx, "admin", f.rule.ID, request, "rename-provisioned-rule")
	if err != nil {
		t.Fatal(err)
	}
	if view.ActivationReason != forwarding.ActivationReasonNoAvailableNodes || f.store.forwardRules[f.rule.ID].ActivationReason != "" {
		t.Fatal("public readiness reason was persisted as configuration state")
	}
	raw, err = f.store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSnapshot(raw); err != nil {
		t.Fatalf("readiness explanation made persisted snapshot unreadable: %v", err)
	}
}

func TestAddingSameGroupRuleRequiresFreshBundleReceiptAndProtocolProof(t *testing.T) {
	f := newVLESSBundleFixture(t, true)
	s := f.store
	ctx := context.Background()
	grantFixtureProtocolHealth(t, s, f.rule.ID, f.endpointID, f.nodeID)
	previous := s.protocolHealth[f.endpointID][f.nodeID]
	if rejected, err := s.PublishProtocolHealthBatch(ctx, []gatewaymembership.ProtocolObservation{previous}); err != nil || rejected[0] != nil {
		t.Fatal("initial protocol activation failed", err)
	}
	assertReason := func(reason forwarding.ActivationReason) {
		t.Helper()
		for refresh := 0; refresh < 2; refresh++ {
			view, err := s.ForwardingRule(ctx, f.rule.ID)
			if err != nil || view.ActivationReason != reason || view.Deployed != (reason == "") {
				t.Fatalf("refresh readiness: reason=%s deployed=%v err=%v want=%s", view.ActivationReason, view.Deployed, err, reason)
			}
		}
		if !s.forwardRules[f.rule.ID].Deployed || s.forwardRules[f.rule.ID].ActivationReason != "" {
			t.Fatal("bundle transition rewrote the existing rule's stored activation")
		}
	}
	assertReason("")
	request := forwarding.Request{Name: "additional same-group rule", CustomerID: f.rule.CustomerID,
		EntryGroupID: f.rule.EntryGroupID, EgressMode: forwarding.EgressDirect, Protocol: forwarding.ProtocolTCP,
		ListenPort: f.rule.ListenPort + 1, Targets: []forwarding.Target{{Host: "landing.example.test", Port: 1080}},
		SelectionPolicy: forwarding.SelectionRoundRobin}
	if _, _, err := forwarding.NewService(s, nil).CreateForAdministrator(ctx, "admin", request, "additional-same-group-rule"); err != nil {
		t.Fatal(err)
	}
	node := s.nodes[f.nodeID]
	if node.DesiredGeneration <= previous.NodeConfigGeneration {
		t.Fatal("new same-group rule did not replace the complete node bundle")
	}
	assertReason(forwarding.ActivationReasonNodeConfigurationPending)
	config := s.nodeConfigsByNode[f.nodeID][node.DesiredGeneration]
	node.AppliedGeneration = node.DesiredGeneration
	s.nodes[f.nodeID] = node
	const attemptID = "0123456789abcdef0123456789abcdef"
	s.applyAttemptsByNode[f.nodeID][node.DesiredGeneration] = generations.ApplyAttemptState{CurrentID: attemptID, SeenIDs: map[string]bool{attemptID: true}}
	results := make(map[string]generations.ApplyResult)
	for _, phase := range []agentv1.ApplyPhase{agentv1.ApplyPhaseCommit, agentv1.ApplyPhaseVerify} {
		results[string(phase)] = generations.ApplyResult{NodeID: f.nodeID, Generation: node.DesiredGeneration, ConfigSHA256: config.ConfigSHA256, Phase: phase, Status: agentv1.ApplyStatusSucceeded, EngineMode: "mixed", AttemptID: attemptID}
	}
	s.applyResultsByNode[f.nodeID][node.DesiredGeneration] = results
	assertReason(forwarding.ActivationReasonProtocolVerificationPending)
	if err := s.PublishProtocolHealth(ctx, previous); err == nil {
		t.Fatal("old protocol proof authorized the replacement bundle")
	}
	fresh := previous
	fresh.NodeConfigGeneration = node.DesiredGeneration
	fresh.ConfigSHA256 = config.ConfigSHA256
	fresh.VerifiedAt = time.Now().UTC()
	fresh.LeaseExpiresAt = fresh.VerifiedAt.Add(endpoints.DefaultHealthTTL)
	if rejected, err := s.PublishProtocolHealthBatch(ctx, []gatewaymembership.ProtocolObservation{fresh}); err != nil || rejected[0] != nil {
		t.Fatal("fresh bundle health was not accepted", err)
	}
	assertReason("")
}
