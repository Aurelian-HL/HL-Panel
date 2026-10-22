package memoryrepo

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	provisioningvless "github.com/hongle/hl-panel/internal/provisioning/vless"
)

func TestProtocolHealthRenewalViewKeepsActivationAndProofsPrivate(t *testing.T) {
	s, proof := protocolPublicationFixture(t)
	ctx := context.Background()
	base := s.ForwardingReadView()
	before, err := base.ProtocolProbeStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	results, changed, activation, err := base.CheckProtocolHealthRenewal(ctx, []gatewaymembership.ProtocolObservation{proof})
	if err != nil || len(results) != 1 || results[0] != nil || !changed || !activation {
		t.Fatalf("first activation lost: %v %v %v %v", results, changed, activation, err)
	}
	after, err := base.ProtocolProbeStates(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("validation mutated cached state", err)
	}
	if _, err = s.PublishProtocolHealthBatch(ctx, []gatewaymembership.ProtocolObservation{proof}); err != nil {
		t.Fatal(err)
	}
	base = s.ForwardingReadView()
	old := proof
	proof.VerifiedAt = proof.VerifiedAt.Add(time.Second)
	proof.LeaseExpiresAt = proof.LeaseExpiresAt.Add(time.Second)
	results, changed, activation, err = base.CheckProtocolHealthRenewal(ctx, []gatewaymembership.ProtocolObservation{proof, old})
	if err != nil || results[0] != nil || results[1] == nil || !changed || activation {
		t.Fatalf("ordered renewal changed: %v %v %v %v", results, changed, activation, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, changed, activation, err := base.CheckProtocolHealthRenewal(ctx, []gatewaymembership.ProtocolObservation{proof})
			if err != nil || results[0] != nil || !changed || activation {
				t.Error("concurrent validation changed evidence", err)
			}
		}()
	}
	wg.Wait()
	states, err := base.ProtocolProbeStates(ctx)
	if err != nil || !states[0].State.ProtocolHealth[proof.NodeID].VerifiedAt.Equal(old.VerifiedAt) {
		t.Fatal("renewal leaked into base", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, changed, _, err := base.CheckProtocolHealthRenewal(cancelled, nil); err == nil || changed {
		t.Fatal("cancelled validation accepted")
	}
}

func TestProtocolHealthRenewalViewRejectsInvalidDeployment(t *testing.T) {
	cases := map[string]func(*Store, *gatewaymembership.ProtocolObservation){
		"paused": func(s *Store, _ *gatewaymembership.ProtocolObservation) {
			r := s.forwardRules["rule"]
			r.Paused = true
			s.forwardRules["rule"] = r
		},
		"customer disabled": func(s *Store, _ *gatewaymembership.ProtocolObservation) {
			c := s.customers["customer"]
			c.Disabled = true
			seedCustomer(s, c)
		},
		"quota exhausted": func(s *Store, _ *gatewaymembership.ProtocolObservation) {
			r := s.forwardRules["rule"]
			r.TrafficLimitBytes = 1
			r.TrafficUsedBytes = 1
			s.forwardRules["rule"] = r
		},
		"config corrupted": func(s *Store, _ *gatewaymembership.ProtocolObservation) {
			c := s.nodeConfigsByNode["node"][1]
			c.Config[0] ^= 1
			s.nodeConfigsByNode["node"][1] = c
		},
		"receipt invalidated": func(s *Store, _ *gatewaymembership.ProtocolObservation) {
			a := s.applyAttemptsByNode["node"][1]
			a.Invalidated = true
			s.applyAttemptsByNode["node"][1] = a
		},
		"old rule":       func(_ *Store, p *gatewaymembership.ProtocolObservation) { p.RuleRevision-- },
		"old generation": func(_ *Store, p *gatewaymembership.ProtocolObservation) { p.NodeConfigGeneration++ },
		"expired": func(_ *Store, p *gatewaymembership.ProtocolObservation) {
			p.VerifiedAt = time.Now().Add(-time.Hour)
			p.LeaseExpiresAt = p.VerifiedAt.Add(time.Second)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s, proof := protocolPublicationFixture(t)
			ctx := context.Background()
			if results, err := s.PublishProtocolHealthBatch(ctx, []gatewaymembership.ProtocolObservation{proof}); err != nil || results[0] != nil {
				t.Fatal("activation failed", results, err)
			}
			proof.VerifiedAt = proof.VerifiedAt.Add(time.Second)
			proof.LeaseExpiresAt = proof.LeaseExpiresAt.Add(time.Second)
			mutate(s, &proof)
			view := s.ForwardingReadView()
			before, beforeAudit := s.PersistenceMutationState()
			results, changed, _, err := view.CheckProtocolHealthRenewal(ctx, []gatewaymembership.ProtocolObservation{proof})
			if err != nil || len(results) != 1 || results[0] == nil || changed {
				t.Fatal("invalid renewal accepted", results, changed, err)
			}
			if err := s.PublishProtocolHealth(ctx, proof); err == nil {
				t.Fatal("authoritative validation accepted invalid renewal")
			}
			after, afterAudit := s.PersistenceMutationState()
			if before != after || beforeAudit != afterAudit {
				t.Fatal("rejected renewal changed state")
			}
		})
	}
}

func BenchmarkProtocolHealthRenewalReadView(b *testing.B) {
	s, proof := protocolPublicationFixture(b)
	hash, err := auth.HashPassword("isolated-benchmark-only")
	if err != nil {
		b.Fatal(err)
	}
	customer := s.customers["customer"]
	customer.PasswordHash = hash
	customer.Revision = 1
	customer.CreatedAt, customer.UpdatedAt = time.Now().UTC(), time.Now().UTC()
	customer.UserGroupID = "users"
	seedCustomer(s, customer)
	s.userGroups["users"] = customers.UserGroup{ID: "users", Name: "users", Revision: 1, AllowedEntryGroupIDs: []string{"group"}, AllowDirect: true, CreatedAt: customer.CreatedAt, UpdatedAt: customer.UpdatedAt}
	s.groupNetworks["group"] = groupconfig.GroupNetwork{GroupID: "group", Revision: 1, ConnectHost: "entry.example.test", PortStart: 443, PortEnd: 443, AllowDirect: true, TrafficMultiplier: 1}
	s.revisionByIdempotency["group"] = map[string]string{}
	rule := s.forwardRules["rule"]
	rule.Name, rule.SelectionPolicy = "health", forwarding.SelectionRoundRobin
	rule.VLESSOutboundMode = forwarding.VLESSOutboundSOCKS5
	rule.VLESSSOCKS5Host, rule.VLESSSOCKS5Port = "127.0.0.1", 1080
	s.vlessSOCKS5Upstreams["rule"] = provisioningvless.SOCKS5Upstream{Hostname: "127.0.0.1", Port: 1080, Username: "isolated", Password: "isolated"}
	rule.RealityDestination = "example.org:443"
	s.forwardRules["rule"] = rule
	admin := s.adminsByUsername["admin"]
	admin.PasswordHash = hash
	s.adminsByUsername["admin"] = admin
	node := s.nodes["node"]
	node.CredentialHash = "isolated-benchmark-node"
	s.nodes["node"] = node
	s.nodesByCredential[node.CredentialHash] = node.ID
	attempt := s.applyAttemptsByNode["node"][1]
	attempt.SeenIDs = map[string]bool{attempt.CurrentID: true}
	s.applyAttemptsByNode["node"][1] = attempt
	ctx := context.Background()
	if _, err := s.PublishProtocolHealthBatch(ctx, []gatewaymembership.ProtocolObservation{proof}); err != nil {
		b.Fatal(err)
	}
	raw, err := s.EncodeSnapshotWithAudit(nil)
	if err != nil {
		b.Fatal(err)
	}
	view := s.ForwardingReadView()
	proof.VerifiedAt = proof.VerifiedAt.Add(time.Second)
	proof.LeaseExpiresAt = proof.LeaseExpiresAt.Add(time.Second)
	b.Run("decode_and_renew", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			state, err := DecodeSnapshot(raw)
			if err != nil {
				b.Fatal(err)
			}
			if err := state.PublishProtocolHealth(ctx, proof); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("view_and_renew", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			results, changed, activation, err := view.CheckProtocolHealthRenewal(ctx, []gatewaymembership.ProtocolObservation{proof})
			if err != nil || results[0] != nil || !changed || activation {
				b.Fatalf("renewal: %v %v", results, err)
			}
		}
	})
}
