package memoryrepo

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

// Freeze the former WithOverlays algorithm, including health restoration, as an
// independent oracle and benchmark. Do not route it through the selective merge.
// The renewal comparison also needs an owned mutable Store, not a read-only view.
func fullCopyForwardingOverlays(v *ForwardingReadView, revision uint64, traffic []RuleTrafficOverlay, health []gatewaymembership.ProtocolObservation, telemetry []nodes.Node) *ForwardingReadView {
	s := v.sharedOverlayState(revision)
	s.nodes = maps.Clone(s.nodes)
	s.forwardRules = maps.Clone(s.forwardRules)
	s.endpointMembers = cloneViewMap(s.endpointMembers, func(m map[string]endpoints.EndpointPoolMember) map[string]endpoints.EndpointPoolMember {
		return maps.Clone(m)
	})
	s.protocolHealth = cloneViewMap(s.protocolHealth, func(m map[string]gatewaymembership.ProtocolObservation) map[string]gatewaymembership.ProtocolObservation {
		return maps.Clone(m)
	})
	s.MergeRuleTrafficOverlays(traffic)
	for _, observation := range health {
		poolID, err := s.endpointPoolIDForRuleLocked(observation.RuleID)
		if err != nil {
			continue
		}
		member, exists := s.endpointMembers[poolID][observation.NodeID]
		if !exists {
			continue
		}
		if previous, exists := s.protocolHealth[poolID][observation.NodeID]; exists && !observation.CanReplace(previous) {
			continue
		}
		if s.protocolHealth[poolID] == nil {
			s.protocolHealth[poolID] = make(map[string]gatewaymembership.ProtocolObservation)
		}
		s.protocolHealth[poolID][observation.NodeID] = observation
		at := observation.VerifiedAt.UTC()
		member.LastHealthAt, member.LastHealthReason, member.UpdatedAt = &at, "protocol_probe", at
		s.endpointMembers[poolID][observation.NodeID] = member
	}
	s.MergeHeartbeatTelemetry(telemetry)
	return &ForwardingReadView{state: s}
}

type forwardingOverlayInputs struct {
	traffic   []RuleTrafficOverlay
	health    []gatewaymembership.ProtocolObservation
	telemetry []nodes.Node
}

func forwardingOverlayFixture(t testing.TB, ruleCount, nodeCount int) (*ForwardingReadView, []gatewaymembership.ProtocolObservation) {
	t.Helper()
	view, proofs := sizedProtocolRenewalFixture(t, ruleCount, nodeCount)
	sortedRenewalProofs(proofs)
	for id, rule := range view.state.forwardRules {
		rule.TrafficLimitBytes, rule.TrafficUsedBytes = 1000, 10
		rule.TrafficQuotaMonthly, rule.TrafficUsagePeriod = true, "2026-10"
		view.state.forwardRules[id] = rule
	}
	for id, node := range view.state.nodes {
		node.CredentialHash = "synthetic-" + id
		node.LastHeartbeatAt = timePointer(proofs[0].VerifiedAt.Add(-time.Minute))
		node.Resources = map[string]any{"nested": map[string]any{"cpu": float64(1)}}
		view.state.nodes[id] = node
	}
	old := append([]gatewaymembership.ProtocolObservation(nil), proofs...)
	for i := range old {
		old[i].VerifiedAt = old[i].VerifiedAt.Add(-2 * time.Second)
		old[i].LeaseExpiresAt = old[i].LeaseExpiresAt.Add(-2 * time.Second)
	}
	return fullCopyForwardingOverlays(view, 9, nil, old, nil), proofs
}

func overlayTraffic(v *ForwardingReadView, id string, used int64, at time.Time) RuleTrafficOverlay {
	rule := v.state.forwardRules[id]
	rule.TrafficUsedBytes = used
	return RuleTrafficOverlay{State: forwarding.TrafficState(rule), UpdatedAt: at}
}

func overlayTelemetry(v *ForwardingReadView, id string, at time.Time) nodes.Node {
	return nodes.Node{ID: id, CredentialHash: v.state.nodes[id].CredentialHash,
		LastHeartbeatAt: timePointer(at), Resources: map[string]any{"nested": map[string]any{"cpu": float64(42)}}}
}

// Capture mutable values deeply, independently of both merge algorithms. Map
// equality deliberately treats nil and empty outer maps as the same state.
func captureForwardingOverlayState(v *ForwardingReadView) *Store {
	return &Store{
		persistenceRevision: v.state.persistenceRevision, gatewayMutationRevision: v.state.gatewayMutationRevision,
		nodes: cloneViewMap(v.state.nodes, cloneNode), forwardRules: cloneViewMap(v.state.forwardRules, cloneForwardingRule),
		endpointMembers: cloneViewMap(v.state.endpointMembers, func(m map[string]endpoints.EndpointPoolMember) map[string]endpoints.EndpointPoolMember {
			return cloneViewMap(m, cloneEndpointMember)
		}),
		protocolHealth: cloneViewMap(v.state.protocolHealth, func(m map[string]gatewaymembership.ProtocolObservation) map[string]gatewaymembership.ProtocolObservation {
			return maps.Clone(m)
		}),
	}
}

func sameForwardingOverlayState(a, b *Store) bool {
	return a.persistenceRevision == b.persistenceRevision && a.gatewayMutationRevision == b.gatewayMutationRevision &&
		reflect.DeepEqual(a.nodes, b.nodes) && reflect.DeepEqual(a.forwardRules, b.forwardRules) &&
		maps.EqualFunc(a.endpointMembers, b.endpointMembers, func(a, b map[string]endpoints.EndpointPoolMember) bool {
			return maps.EqualFunc(a, b, func(a, b endpoints.EndpointPoolMember) bool { return reflect.DeepEqual(a, b) })
		}) && maps.EqualFunc(a.protocolHealth, b.protocolHealth, func(a, b map[string]gatewaymembership.ProtocolObservation) bool { return maps.Equal(a, b) })
}

func TestForwardingOverlaysSelectiveCopyMatchesFullCopy(t *testing.T) {
	for _, scenario := range []string{
		"empty", "traffic", "traffic order", "traffic missing", "traffic revision", "traffic month", "traffic period", "traffic quota crossing",
		"health single", "health same pool", "health all", "health order", "health missing pool", "health missing member", "health duplicate pool",
		"health changed deployment", "health expired restore", "health nil map", "health all ignored",
		"telemetry", "telemetry order", "telemetry identity", "telemetry deleted", "telemetry stale", "telemetry absent", "telemetry nil time", "mixed",
	} {
		t.Run(scenario, func(t *testing.T) {
			view, proofs := forwardingOverlayFixture(t, 4, 3)
			traffic := overlayTraffic(view, proofs[0].RuleID, 25, proofs[0].VerifiedAt)
			telemetry := overlayTelemetry(view, proofs[0].NodeID, proofs[0].VerifiedAt)
			in := forwardingOverlayInputs{}
			switch scenario {
			case "traffic":
				in.traffic = []RuleTrafficOverlay{traffic}
			case "traffic order":
				later, smaller := traffic, traffic
				later.State.TrafficUsedBytes, smaller.State.TrafficUsedBytes = 50, 20
				in.traffic = []RuleTrafficOverlay{traffic, later, traffic, smaller}
			case "traffic missing":
				traffic.State.ID = "missing"
				in.traffic = []RuleTrafficOverlay{traffic}
			case "traffic revision":
				traffic.State.Revision--
				in.traffic = []RuleTrafficOverlay{traffic}
			case "traffic month":
				traffic.State.TrafficQuotaMonthly = false
				in.traffic = []RuleTrafficOverlay{traffic}
			case "traffic period":
				traffic.State.TrafficUsagePeriod = "2026-09"
				in.traffic = []RuleTrafficOverlay{traffic}
			case "traffic quota crossing":
				traffic.State.TrafficUsedBytes = 1000
				in.traffic = []RuleTrafficOverlay{traffic}
			case "health single":
				in.health = proofs[:1]
			case "health same pool":
				in.health = proofs[:3]
			case "health all":
				in.health = proofs
			case "health order":
				newer, stale, extension := proofs[0], proofs[0], proofs[0]
				newer.VerifiedAt, newer.LeaseExpiresAt = newer.VerifiedAt.Add(time.Second), newer.LeaseExpiresAt.Add(time.Second)
				stale.VerifiedAt, stale.LeaseExpiresAt = stale.VerifiedAt.Add(-time.Minute), stale.LeaseExpiresAt.Add(-time.Minute)
				extension.LeaseExpiresAt = extension.LeaseExpiresAt.Add(2 * time.Second)
				in.health = []gatewaymembership.ProtocolObservation{proofs[0], extension, proofs[0], newer, stale, proofs[1], proofs[0]}
			case "health missing pool":
				p := proofs[0]
				p.RuleID = "missing"
				in.health = []gatewaymembership.ProtocolObservation{p}
			case "health missing member":
				p := proofs[0]
				p.NodeID = "missing"
				in.health = []gatewaymembership.ProtocolObservation{p}
			case "health duplicate pool":
				p := view.state.endpointPools["pool-"+proofs[0].RuleID]
				p.ID = "duplicate"
				view.state.endpointPools = maps.Clone(view.state.endpointPools)
				view.state.endpointPools[p.ID] = p
				view.state.forwardingProjection = &forwardingProjection{}
				in.health = proofs[:1]
			case "health changed deployment":
				p := proofs[0]
				p.RuleRevision++
				p.NodeConfigGeneration++
				p.VerifiedAt, p.LeaseExpiresAt = p.VerifiedAt.Add(-time.Minute), p.LeaseExpiresAt.Add(-time.Minute)
				in.health = []gatewaymembership.ProtocolObservation{p}
			case "health expired restore":
				view.state.protocolHealth = nil
				p := proofs[0]
				p.VerifiedAt = p.VerifiedAt.Add(-time.Hour)
				p.LeaseExpiresAt = p.VerifiedAt.Add(time.Second)
				in.health = []gatewaymembership.ProtocolObservation{p}
			case "health nil map":
				view.state.protocolHealth = nil
				in.health = proofs[:3]
			case "health all ignored":
				for _, p := range proofs {
					p.VerifiedAt, p.LeaseExpiresAt = p.VerifiedAt.Add(-time.Minute), p.LeaseExpiresAt.Add(-time.Minute)
					in.health = append(in.health, p)
				}
			case "telemetry":
				in.telemetry = []nodes.Node{telemetry}
			case "telemetry order":
				later := overlayTelemetry(view, proofs[0].NodeID, telemetry.LastHeartbeatAt.Add(time.Second))
				in.telemetry = []nodes.Node{telemetry, later, telemetry}
			case "telemetry identity":
				telemetry.CredentialHash = "retired"
				in.telemetry = []nodes.Node{telemetry}
			case "telemetry deleted":
				node := view.state.nodes[telemetry.ID]
				node.DeletedAt = timePointer(proofs[0].VerifiedAt)
				view.state.nodes[telemetry.ID] = node
				in.telemetry = []nodes.Node{telemetry}
			case "telemetry stale":
				telemetry.LastHeartbeatAt = timePointer(proofs[0].VerifiedAt.Add(-2 * time.Minute))
				in.telemetry = []nodes.Node{telemetry}
			case "telemetry absent":
				telemetry.ID = "missing"
				in.telemetry = []nodes.Node{telemetry}
			case "telemetry nil time":
				telemetry.LastHeartbeatAt = nil
				in.telemetry = []nodes.Node{telemetry}
			case "mixed":
				in = forwardingOverlayInputs{[]RuleTrafficOverlay{traffic}, proofs[:4], []nodes.Node{telemetry}}
			}
			before := captureForwardingOverlayState(view)
			want := fullCopyForwardingOverlays(view, 18, in.traffic, in.health, in.telemetry)
			got := view.WithOverlays(18, in.traffic, in.health, in.telemetry)
			if !sameForwardingOverlayState(want.state, got.state) {
				t.Fatal("selective merge changed restored rules, nodes, leases or revisions")
			}
			if !sameForwardingOverlayState(before, view.state) {
				t.Fatal("overlay changed its source view")
			}
			for id := range view.state.forwardRules {
				wantRule, wantErr := want.ForwardingRule(context.Background(), id)
				gotRule, gotErr := got.ForwardingRule(context.Background(), id)
				if fmt.Sprint(wantErr) != fmt.Sprint(gotErr) || !reflect.DeepEqual(wantRule, gotRule) {
					t.Fatal("public rule projection changed", id, wantErr, gotErr)
				}
			}
			if scenario == "health expired restore" {
				p := got.state.protocolHealth["pool-"+proofs[0].RuleID][proofs[0].NodeID]
				if p != in.health[0] || p.LeaseExpiresAt.After(time.Now()) {
					t.Fatal("restoration dropped or extended an expired committed lease")
				}
			}
		})
	}
}

func TestForwardingOverlaysSelectiveCopyChainsAndInputsStayIsolated(t *testing.T) {
	base, proofs := forwardingOverlayFixture(t, 4, 3)
	before := captureForwardingOverlayState(base)
	traffic := overlayTraffic(base, proofs[0].RuleID, 25, proofs[0].VerifiedAt)
	telemetry := overlayTelemetry(base, proofs[0].NodeID, proofs[0].VerifiedAt)
	first := base.WithOverlays(10, []RuleTrafficOverlay{traffic}, proofs[:1], []nodes.Node{telemetry})
	firstBefore := captureForwardingOverlayState(first)
	// Callers own these pointers and nested maps after the merge returns.
	*telemetry.LastHeartbeatAt = telemetry.LastHeartbeatAt.Add(time.Hour)
	telemetry.Resources["nested"].(map[string]any)["cpu"] = float64(999)
	if !sameForwardingOverlayState(firstBefore, first.state) {
		t.Fatal("caller telemetry input mutated the view")
	}
	newer := proofs[0]
	newer.VerifiedAt, newer.LeaseExpiresAt = newer.VerifiedAt.Add(time.Second), newer.LeaseExpiresAt.Add(time.Second)
	second := first.WithOverlays(11, []RuleTrafficOverlay{overlayTraffic(first, proofs[0].RuleID, 50, newer.VerifiedAt)},
		[]gatewaymembership.ProtocolObservation{newer, proofs[3]}, []nodes.Node{overlayTelemetry(first, proofs[0].NodeID, newer.VerifiedAt)})
	emptySibling := first.WithOverlays(12, nil, nil, nil)
	sibling := base.WithOverlays(13, nil, proofs[1:3], nil)
	if second.state.forwardRules[proofs[0].RuleID].TrafficUsedBytes != 50 || second.state.protocolHealth["pool-"+proofs[0].RuleID][proofs[0].NodeID] != newer ||
		sibling.state.protocolHealth["pool-"+proofs[1].RuleID][proofs[1].NodeID] != proofs[1] {
		t.Fatal("chain or sibling lost its private update")
	}
	expectedEmpty := captureForwardingOverlayState(first)
	expectedEmpty.persistenceRevision = 12
	if !sameForwardingOverlayState(before, base.state) || !sameForwardingOverlayState(firstBefore, first.state) || !sameForwardingOverlayState(expectedEmpty, emptySibling.state) {
		t.Fatal("descendant or sibling changed an earlier view")
	}
	poolID := "pool-" + proofs[0].RuleID
	dto, err := first.GatewayMembershipState(context.Background(), poolID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range dto.Members {
		if dto.Members[i].LastHealthAt != nil {
			*dto.Members[i].LastHealthAt = time.Time{}
		}
		dto.Members[i].DialHost = "changed"
	}
	delete(dto.ProtocolHealth, proofs[0].NodeID)
	if !sameForwardingOverlayState(firstBefore, first.state) || !sameForwardingOverlayState(before, base.state) {
		t.Fatal("public DTO mutated shared member pointers or health")
	}
}

func TestForwardingOverlaysSelectiveCopyConcurrentSiblings(t *testing.T) {
	base, proofs := forwardingOverlayFixture(t, 4, 3)
	before := captureForwardingOverlayState(base)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			proof := proofs[i%len(proofs)]
			for range 10 {
				local := base.WithOverlays(uint64(20+i), []RuleTrafficOverlay{overlayTraffic(base, proof.RuleID, int64(30+i), proof.VerifiedAt)},
					[]gatewaymembership.ProtocolObservation{proof}, []nodes.Node{overlayTelemetry(base, proof.NodeID, proof.VerifiedAt)})
				prior := captureForwardingOverlayState(local)
				newer := proof
				newer.VerifiedAt, newer.LeaseExpiresAt = newer.VerifiedAt.Add(time.Second), newer.LeaseExpiresAt.Add(time.Second)
				next := local.WithOverlays(uint64(40+i), nil, []gatewaymembership.ProtocolObservation{newer}, nil)
				if !sameForwardingOverlayState(prior, local.state) || next.state.protocolHealth["pool-"+proof.RuleID][proof.NodeID] != newer {
					t.Error("concurrent overlay chain contaminated its parent or lost proof")
					return
				}
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if !sameForwardingOverlayState(before, base.state) {
		t.Fatal("concurrent siblings changed cached source state")
	}
}

var forwardingOverlayCopySink *ForwardingReadView

func BenchmarkForwardingOverlayCopy(b *testing.B) {
	for _, scenario := range []struct {
		name         string
		rules, nodes int
		health       int
		traffic      bool
		telemetry    bool
		ignored      bool
	}{
		{"empty", 42, 28, 0, false, false, false},
		{"traffic", 42, 28, 0, true, false, false},
		{"health_single", 42, 28, 1, false, false, false},
		{"health_same_pool_28", 42, 28, 28, false, false, false},
		{"health_32", 42, 28, 32, false, false, false},
		{"health_all_1176", 42, 28, 1176, false, false, false},
		{"health_ignored_32", 42, 28, 32, false, false, true},
		{"telemetry", 42, 28, 0, false, true, false},
		{"mixed", 42, 28, 32, true, true, false},
		{"mixed_all_1176", 42, 28, 1176, true, true, false},
		{"pools_500_single", 500, 1, 1, false, false, false},
	} {
		b.Run(scenario.name, func(b *testing.B) {
			view, proofs := forwardingOverlayFixture(b, scenario.rules, scenario.nodes)
			before := captureForwardingOverlayState(view)
			in := forwardingOverlayInputs{health: proofs[:scenario.health]}
			if scenario.traffic {
				in.traffic = []RuleTrafficOverlay{overlayTraffic(view, proofs[0].RuleID, 25, proofs[0].VerifiedAt)}
			}
			if scenario.telemetry {
				in.telemetry = []nodes.Node{overlayTelemetry(view, proofs[0].NodeID, proofs[0].VerifiedAt)}
			}
			if scenario.ignored {
				in.health = append([]gatewaymembership.ProtocolObservation(nil), in.health...)
				for i := range in.health {
					in.health[i].VerifiedAt = in.health[i].VerifiedAt.Add(-time.Minute)
					in.health[i].LeaseExpiresAt = in.health[i].LeaseExpiresAt.Add(-time.Minute)
				}
			}
			want := fullCopyForwardingOverlays(view, 18, in.traffic, in.health, in.telemetry)
			for _, mode := range []string{"full_copy", "selective_copy"} {
				merge := fullCopyForwardingOverlays
				if mode == "selective_copy" {
					merge = (*ForwardingReadView).WithOverlays
				}
				b.Run(mode, func(b *testing.B) {
					// Equal warm structural indexes, excluding fixture setup.
					merge(view, 18, nil, proofs[:1], nil)
					merge(view, 18, nil, proofs[:1], nil)
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						forwardingOverlayCopySink = merge(view, 18, in.traffic, in.health, in.telemetry)
					}
					b.StopTimer()
					if !sameForwardingOverlayState(want.state, forwardingOverlayCopySink.state) || !sameForwardingOverlayState(before, view.state) {
						b.Fatal("merge changed immutable source or diverged from frozen full copy")
					}
				})
			}
		})
	}
}
