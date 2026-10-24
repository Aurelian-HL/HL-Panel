package memoryrepo

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/vlessconnection"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
)

func connectionReadFixture(t testing.TB) (*Store, gatewaymembership.ProtocolObservation) {
	t.Helper()
	s, proof := protocolPublicationFixture(t)
	r := s.forwardRules["rule"]
	r.Name, r.RealityDestination = "isolated", "example.org:443"
	r.OwnerKind, r.OwnerID = forwarding.OwnerCustomer, "customer"
	s.forwardRules[r.ID] = r
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	s.businessIdempotency["admin\x00forwarding.create\x00delegated"] = businessMutation{ResourceID: r.ID, Result: data}
	p := s.endpointPools["pool"]
	p.OwnerID = "admin"
	s.endpointPools[p.ID] = p
	results, err := s.PublishProtocolHealthBatch(context.Background(), []gatewaymembership.ProtocolObservation{proof})
	if err != nil || len(results) != 1 || results[0] != nil {
		t.Fatal("fixture activation failed", results, err)
	}
	return s, proof
}

func TestConnectionReadViewPreservesLiveReadinessAndPermissions(t *testing.T) {
	cases := map[string]func(*Store){
		"healthy":  func(*Store) {},
		"paused":   func(s *Store) { r := s.forwardRules["rule"]; r.Paused = true; s.forwardRules[r.ID] = r },
		"disabled": func(s *Store) { c := s.customers["customer"]; c.Disabled = true; s.customers[c.ID] = c },
		"expired customer": func(s *Store) {
			c := s.customers["customer"]
			at := time.Now().Add(-time.Hour)
			c.ExpiresAt = &at
			seedCustomer(s, c)
		},
		"exhausted quota": func(s *Store) {
			r := s.forwardRules["rule"]
			r.TrafficLimitBytes, r.TrafficUsedBytes = 1, 1
			s.forwardRules[r.ID] = r
		},
		"invalid receipt": func(s *Store) {
			a := s.applyAttemptsByNode["node"][1]
			a.Invalidated = true
			s.applyAttemptsByNode["node"][1] = a
		},
		"corrupt bundle": func(s *Store) {
			c := s.nodeConfigsByNode["node"][1]
			c.Config[0] ^= 1
			s.nodeConfigsByNode["node"][1] = c
		},
		"expired proof": func(s *Store) {
			p := s.protocolHealth["pool"]["node"]
			p.VerifiedAt = time.Now().Add(-time.Hour)
			p.LeaseExpiresAt = p.VerifiedAt.Add(time.Second)
			s.protocolHealth["pool"]["node"] = p
		},
		"revoked identity": func(s *Store) {
			c := s.vlessBindings["binding"]
			c.Binding.State = vlessidentity.StateRevoked
			s.vlessBindings["binding"] = c
		},
		"customer authored": func(s *Store) { delete(s.businessIdempotency, "admin\x00forwarding.create\x00delegated") },
		"foreign endpoint":  func(s *Store) { p := s.endpointPools["pool"]; p.OwnerID = "other"; s.endpointPools[p.ID] = p },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s, _ := connectionReadFixture(t)
			mutate(s)
			identity, err := vlessidentity.NewService(s, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			legacy := vlessconnection.NewService(identity, forwarding.NewService(s, nil), endpoints.NewServiceWithReadyCandidateSource(s, nil, gatewaymembership.NewService(s)))
			want, wantErr := legacy.ResolveForwardingConnection(context.Background(), "admin", "rule")
			view := s.ForwardingReadView().WithOverlays(1, nil, nil, nil)
			got, err := view.ResolveForwardingConnection(context.Background(), "admin", "rule")
			if got != want || !sameConnectionError(err, wantErr) {
				t.Fatalf("forwarding view changed result: got error %v; ordinary error %v", err, wantErr)
			}
			byID, byErr := view.ResolveIdentityConnection(context.Background(), "admin", "binding")
			if byID != want || !sameConnectionError(byErr, wantErr) {
				t.Fatalf("identity view changed result: %v", byErr)
			}
			if (err == nil) != (name == "healthy" || name == "expired customer") {
				t.Fatalf("unexpected admission: %v", err)
			}
			if err != nil && got.URI != "" {
				t.Fatal("failed admission returned a URI")
			}
			if _, err := view.ResolveIdentityConnection(context.Background(), "other", "binding"); !errors.Is(err, faults.ErrNotFound) {
				t.Fatal("foreign administrator gained connection access")
			}
			lines, rejected, batchErr := view.ResolveSubscriptionLines(context.Background(), "admin", "customer", []string{"binding", "missing"})
			if batchErr != nil || lines["binding"] != want.URI || !sameConnectionError(rejected["binding"], wantErr) || !errors.Is(rejected["missing"], faults.ErrNotFound) {
				t.Fatal("subscription batch changed readiness or ownership", batchErr)
			}
			lines, rejected, batchErr = view.ResolveSubscriptionLines(context.Background(), "admin", "foreign-customer", []string{"binding"})
			if batchErr != nil || len(lines) != 0 || !errors.Is(rejected["binding"], faults.ErrNotFound) {
				t.Fatal("subscription exposed a different customer's identity")
			}
		})
	}
}

func sameConnectionError(a, b error) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && a.Error() == b.Error())
}

func TestManagedSingleRuleMatchesListAuthorization(t *testing.T) {
	s, _ := connectionReadFixture(t)
	for _, admin := range []string{"admin", "other", ""} {
		for _, repo := range []interface {
			ManagedForwardingRule(context.Context, string, string) (forwarding.Rule, error)
			ListManagedForwardingRules(context.Context, string) ([]forwarding.Rule, error)
		}{s, s.ForwardingReadView()} {
			list, err := repo.ListManagedForwardingRules(context.Background(), admin)
			if err != nil {
				t.Fatal(err)
			}
			got, err := repo.ManagedForwardingRule(context.Background(), admin, "rule")
			if len(list) == 0 {
				if !errors.Is(err, faults.ErrNotFound) {
					t.Fatal("single rule bypassed list permissions", err)
				}
			} else if err != nil || !reflect.DeepEqual(list[0], got) {
				t.Fatal("single rule diverged from authorized list", err)
			}
		}
	}
}

func TestConnectionReadViewRechecksLeaseAndPrivateOverlay(t *testing.T) {
	s, proof := connectionReadFixture(t)
	r := s.forwardRules["rule"]
	r.TrafficLimitBytes = 100
	s.forwardRules[r.ID] = r
	base := s.ForwardingReadView()
	r.TrafficUsedBytes = 99
	limited := base.WithOverlays(2, []RuleTrafficOverlay{{State: forwarding.TrafficState(r)}}, nil, nil)
	got, err := limited.ForwardingRule(context.Background(), "rule")
	if err != nil || got.TrafficUsedBytes != 99 {
		t.Fatal("latest usage overlay was hidden", err)
	}
	clean, err := base.ForwardingRule(context.Background(), "rule")
	if err != nil || clean.TrafficUsedBytes != 0 {
		t.Fatal("private overlay contaminated base view", err)
	}
	if _, err := limited.ResolveForwardingConnection(context.Background(), "admin", "rule"); err != nil {
		t.Fatal("valid usage overlay changed readiness", err)
	}
	// The exact same cached inputs must stop issuing URIs when the lease expires.
	proof.LeaseExpiresAt = time.Now().Add(200 * time.Millisecond)
	base.state.protocolHealth["pool"]["node"] = proof
	if _, err := base.ResolveIdentityConnection(context.Background(), "admin", "binding"); err != nil {
		t.Fatal("fixture not ready before expiry", err)
	}
	time.Sleep(time.Until(proof.LeaseExpiresAt) + time.Millisecond)
	if got, err := base.ResolveIdentityConnection(context.Background(), "admin", "binding"); err == nil || got.URI != "" {
		t.Fatal("URI served after lease expiry")
	}
}
