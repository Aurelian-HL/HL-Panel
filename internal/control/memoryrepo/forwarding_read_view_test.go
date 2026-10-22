package memoryrepo

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestForwardingReadViewDetachedAndRechecksEvidence(t *testing.T) {
	cases := map[string]func(*Store){
		"healthy":           func(*Store) {},
		"paused":            func(s *Store) { r := s.forwardRules["rule"]; r.Paused = true; s.forwardRules["rule"] = r },
		"customer disabled": func(s *Store) { c := s.customers["customer"]; c.Disabled = true; s.customers["customer"] = c },
		"quota exhausted": func(s *Store) {
			r := s.forwardRules["rule"]
			r.TrafficLimitBytes = 1
			r.TrafficUsedBytes = 1
			s.forwardRules["rule"] = r
		},
		"receipt invalidated": func(s *Store) {
			a := s.applyAttemptsByNode["node"][1]
			a.Invalidated = true
			s.applyAttemptsByNode["node"][1] = a
		},
		"config corrupted": func(s *Store) {
			c := s.nodeConfigsByNode["node"][1]
			c.Config[0] ^= 1
			s.nodeConfigsByNode["node"][1] = c
		},
		"health expired": func(s *Store) {
			p := s.protocolHealth["pool"]["node"]
			p.VerifiedAt = time.Now().Add(-time.Hour)
			p.LeaseExpiresAt = p.VerifiedAt.Add(time.Second)
			s.protocolHealth["pool"]["node"] = p
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s, proof := protocolPublicationFixture(t)
			if _, err := s.PublishProtocolHealthBatch(context.Background(), []gatewaymembership.ProtocolObservation{proof}); err != nil {
				t.Fatal(err)
			}
			change(s)
			view := s.ForwardingReadView()
			expected, err := s.ForwardingRule(context.Background(), "rule")
			if err != nil {
				t.Fatal(err)
			}
			actual, err := view.ForwardingRule(context.Background(), "rule")
			if err != nil || !reflect.DeepEqual(actual, expected) {
				t.Fatal("projection changed readiness", err)
			}
			if (actual.Status == forwarding.StatusActive) != (name == "healthy") {
				t.Fatalf("unexpected readiness %s", actual.Status)
			}
			// Mutations after publication cannot alter an existing detached view.
			delete(s.forwardRules, "rule")
			delete(s.nodes, "node")
			again, err := view.ForwardingRule(context.Background(), "rule")
			if err != nil || !reflect.DeepEqual(again, expected) {
				t.Fatal("source mutation reached view", err)
			}
		})
	}
}

func TestForwardingReadViewPrivateOverlaysAndReturnedDTOs(t *testing.T) {
	s, proof := protocolPublicationFixture(t)
	ctx := context.Background()
	if err := s.PublishProtocolHealth(ctx, proof); err != nil {
		t.Fatal(err)
	}
	base := s.ForwardingReadView()
	r := s.forwardRules["rule"]
	r.TrafficUsedBytes = 99
	stale := proof
	stale.VerifiedAt = time.Now().Add(-time.Hour)
	stale.LeaseExpiresAt = stale.VerifiedAt.Add(time.Second)
	// A later committed expired proof must also remain expired on every read.
	base.state.protocolHealth["pool"] = nil
	overlay := base.WithOverlays(71, []RuleTrafficOverlay{{State: forwarding.TrafficState(r)}}, []gatewaymembership.ProtocolObservation{stale}, nil)
	actual, err := overlay.ForwardingRule(ctx, "rule")
	if err != nil || actual.TrafficUsedBytes != 99 || actual.Status == forwarding.StatusActive {
		t.Fatal("overlay hidden or expired health granted readiness", err)
	}
	clean, err := base.ForwardingRule(ctx, "rule")
	if err != nil || clean.TrafficUsedBytes != 0 {
		t.Fatal("overlay contaminated cache", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				view := base.WithOverlays(72, nil, []gatewaymembership.ProtocolObservation{proof}, []nodes.Node{{ID: "node", LastHeartbeatAt: &proof.VerifiedAt, Resources: map[string]any{"cpu": 1}}})
				state, err := view.GatewayMembershipState(ctx, "pool")
				if err != nil {
					t.Error(err)
					return
				}
				state.Pool.Name = "changed"
				state.Members[0].DialHost = "changed"
				delete(state.ProtocolHealth, "node")
				again, err := view.GatewayMembershipState(ctx, "pool")
				if err != nil || again.Members[0].DialHost == "changed" || len(again.ProtocolHealth) != 1 {
					t.Error("returned DTO contaminated cached state", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestDeploymentInputPublicConfigAndAttemptMapsAreDetached(t *testing.T) {
	s, _ := protocolPublicationFixture(t)
	ctx := context.Background()
	node := s.nodes["node"]
	node.Resources = map[string]any{"nested": map[string]any{"cpu": float64(1)}}
	s.nodes["node"] = node
	a := s.applyAttemptsByNode["node"][1]
	a.SeenIDs = map[string]bool{"original": true}
	s.applyAttemptsByNode["node"][1] = a
	s.groupNetworks["group"] = groupconfig.GroupNetwork{GroupID: "group", ConnectHost: "127.0.0.1", PortStart: 443, PortEnd: 443,
		PortRanges: []groupconfig.PortRange{{Start: 443, End: 443}}, AllowedExitGroupIDs: []string{"exit"}}
	rule := s.forwardRules["rule"]
	rule.ExitGroupID = "exit"
	s.forwardRules["rule"] = rule
	s.groupNetworks["exit"] = groupconfig.GroupNetwork{GroupID: "exit", AllowedEntryGroupIDs: []string{"group"}}
	view := s.ForwardingReadView()
	s.nodeConfigsByNode["node"][2] = s.nodeConfigsByNode["node"][1]
	if len(view.state.nodeConfigsByNode["node"]) != 1 {
		t.Fatal("historical generations retained in projection")
	}
	s.applyAttemptsByNode["node"][1].SeenIDs["leak"] = true
	if view.state.applyAttemptsByNode["node"][1].SeenIDs["leak"] {
		t.Fatal("attempt map shared with source")
	}
	for name, read := range map[string]func(context.Context, string, string) (deploymentreceipts.Input, error){
		"repository": s.RuleNodeDeploymentInput, "projection": view.RuleNodeDeploymentInput,
	} {
		t.Run(name, func(t *testing.T) {
			first, err := read(ctx, "rule", "node")
			if err != nil {
				t.Fatal(err)
			}
			first.CurrentConfig.Config[0] ^= 1
			first.Node.Resources["nested"].(map[string]any)["cpu"] = float64(999)
			first.EntryNetwork.AllowedExitGroupIDs[0] = "changed"
			first.EntryNetwork.PortRanges[0].Start = 1
			first.ExitNetwork.AllowedEntryGroupIDs[0] = "changed"
			delete(first.ApplyResults, agentv1.ApplyPhaseVerify)
			again, err := read(ctx, "rule", "node")
			if err != nil || generations.SHA256Hex(again.CurrentConfig.Config) != again.CurrentConfig.ConfigSHA256 {
				t.Fatal("public caller changed authoritative config", err)
			}
			if again.Node.Resources["nested"].(map[string]any)["cpu"] != float64(1) || again.EntryNetwork.AllowedExitGroupIDs[0] != "exit" ||
				again.EntryNetwork.PortRanges[0].Start != 443 || again.ExitNetwork.AllowedEntryGroupIDs[0] != "group" || len(again.ApplyResults) != 2 {
				t.Fatal("public caller changed authoritative nested inputs")
			}
		})
	}
}

func TestDeploymentReadViewPreservesAppliedFallbackAndInvalidation(t *testing.T) {
	s, _ := protocolPublicationFixture(t)
	n := s.nodes["node"]
	n.DesiredGeneration = 2
	s.nodes["node"] = n
	config := cloneNodeConfig(s.nodeConfigsByNode["node"][1])
	config.Generation = 2
	s.nodeConfigsByNode["node"][2] = config
	s.applyAttemptsByNode["node"][2] = generations.ApplyAttemptState{CurrentID: "next", Invalidated: true}
	want, err := s.RuleNodeDeploymentInput(context.Background(), "rule", "node")
	if err != nil {
		t.Fatal(err)
	}
	view := s.ForwardingReadView()
	got, err := view.RuleNodeDeploymentInput(context.Background(), "rule", "node")
	if err != nil || !reflect.DeepEqual(got, want) || got.AppliedConfig == nil || !got.AttemptInvalidated {
		t.Fatal("projection lost applied fallback or invalidation", err)
	}
	got.AppliedConfig.Config[0] ^= 1
	delete(got.AppliedResults, agentv1.ApplyPhaseCommit)
	again, err := view.RuleNodeDeploymentInput(context.Background(), "rule", "node")
	if err != nil || !reflect.DeepEqual(again, want) {
		t.Fatal("caller mutated applied fallback", err)
	}
}

func TestForwardingReadViewDoesNotCacheActivationPastLeaseExpiry(t *testing.T) {
	s, proof := protocolPublicationFixture(t)
	ctx := context.Background()
	proof.LeaseExpiresAt = time.Now().Add(time.Second)
	if _, err := s.PublishProtocolHealthBatch(ctx, []gatewaymembership.ProtocolObservation{proof}); err != nil {
		t.Fatal(err)
	}
	view := s.ForwardingReadView()
	before, err := view.ForwardingRule(ctx, "rule")
	if err != nil || before.Status != forwarding.StatusActive {
		t.Fatal("fixture not active before lease expiry", err)
	}
	time.Sleep(time.Until(proof.LeaseExpiresAt) + time.Millisecond)
	after, err := view.ForwardingRule(ctx, "rule")
	if err != nil || after.Status == forwarding.StatusActive || after.Deployed {
		t.Fatal("immutable view cached activation after lease expiry", err)
	}
}
