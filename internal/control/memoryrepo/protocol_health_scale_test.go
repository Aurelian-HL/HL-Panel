package memoryrepo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// Reproduce a shared group with 42 rules and 28 nodes without production data.
func scaledProtocolRenewalFixture(t testing.TB) (*ForwardingReadView, []gatewaymembership.ProtocolObservation) {
	return sizedProtocolRenewalFixture(t, 42, 28)
}

func sizedProtocolRenewalFixture(t testing.TB, ruleCount, nodeCount int) (*ForwardingReadView, []gatewaymembership.ProtocolObservation) {
	t.Helper()
	s, proof := protocolPublicationFixture(t)
	rule, pool, node := s.forwardRules["rule"], s.endpointPools["pool"], s.nodes["node"]
	config := s.nodeConfigsByNode["node"][1]
	attempt := s.applyAttemptsByNode["node"][1]
	var bundle agentv1.ConfigurationBundle
	bundle.SchemaVersion = 1
	for i := 0; i < ruleCount; i++ {
		id := fmt.Sprintf("rule-%02d", i)
		r := rule
		r.ID, r.Deployed, r.ListenPort = id, true, 10000+i
		s.forwardRules[id] = r
		p := pool
		p.ID, p.RuleID, p.Port = "pool-"+id, id, r.ListenPort
		s.endpointPools[p.ID] = p
		s.endpointMembers[p.ID] = make(map[string]endpoints.EndpointPoolMember)
		bundle.Fragments = append(bundle.Fragments, agentv1.ConfigurationFragment{
			GroupID: "forwarding-vless/" + id, GroupRevision: 3, Engine: agentv1.EngineXray,
			Config: json.RawMessage(`{"inbounds":[],"padding":"` + strings.Repeat("x", 1024) + `"}`),
		})
	}
	delete(s.forwardRules, "rule")
	delete(s.endpointPools, "pool")
	delete(s.endpointMembers, "pool")
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	config.Config, config.ConfigSHA256 = raw, generations.SHA256Hex(raw)
	var proofs []gatewaymembership.ProtocolObservation
	for i := 0; i < nodeCount; i++ {
		id := fmt.Sprintf("node-%02d", i)
		n := node
		n.ID = id
		s.nodes[id] = n
		s.membersByGroup["group"][id] = groups.Member{GroupID: "group", NodeID: id}
		c := config
		c.NodeID = id
		s.nodeConfigsByNode[id] = map[int64]generations.NodeConfigGeneration{1: c}
		s.applyAttemptsByNode[id] = map[int64]generations.ApplyAttemptState{1: attempt}
		s.applyResultsByNode[id] = map[int64]map[string]generations.ApplyResult{1: {}}
		for phase, receipt := range s.applyResultsByNode["node"][1] {
			receipt.NodeID, receipt.ConfigSHA256 = id, c.ConfigSHA256
			s.applyResultsByNode[id][1][phase] = receipt
		}
		for _, p := range s.endpointPools {
			s.endpointMembers[p.ID][id] = endpoints.EndpointPoolMember{PoolID: p.ID, GroupID: "group", NodeID: id, DialHost: proof.DialHost, Weight: 1, State: endpoints.CandidateEligible}
			o := proof
			o.RuleID, o.NodeID, o.ConfigSHA256 = p.RuleID, id, c.ConfigSHA256
			proofs = append(proofs, o)
		}
	}
	delete(s.nodes, "node")
	delete(s.membersByGroup["group"], "node")
	return s.ForwardingReadView(), proofs
}

func BenchmarkProtocolHealthRenewalSharedGroup(b *testing.B) {
	view, proofs := scaledProtocolRenewalFixture(b)
	proofs = proofs[:32]
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		results, changed, activation, err := view.CheckProtocolHealthRenewal(context.Background(), proofs)
		if err != nil || !changed || activation {
			b.Fatalf("renewal: %v %v %v", err, changed, activation)
		}
		for _, err := range results {
			if err != nil {
				b.Fatal(err)
			}
		}
	}
}

func TestProtocolHealthRenewalAfterLeaseLossDoesNotReactivateSharedGroup(t *testing.T) {
	view, proofs := scaledProtocolRenewalFixture(t)
	// No live leases exist, just as after recovery from an overloaded runner.
	// The durable rules have already been activated. Renewals must use the
	// small proof overlay without changing their audited activation state.
	results, changed, activation, err := view.CheckProtocolHealthRenewal(context.Background(), proofs[:32])
	if err != nil || !changed || activation {
		t.Fatalf("renewal: %v %v %v", err, changed, activation)
	}
	for _, err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
}
