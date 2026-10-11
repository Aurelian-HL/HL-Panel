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
