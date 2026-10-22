package memoryrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/vlessconnection"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
)

func TestConfigurationRead121PausedExpiredRulesHasNoSideEffects(t *testing.T) {
	s, _ := connectionReadFixture(t)
	rule, pool, binding := s.forwardRules["rule"], s.endpointPools["pool"], s.vlessBindings["binding"]
	delete(s.forwardRules, "rule")
	delete(s.endpointPools, "pool")
	delete(s.vlessBindings, "binding")
	clear(s.businessIdempotency)
	clear(s.vlessRuntimeMaterials)
	clear(s.protocolHealth)
	c := s.customers["customer"]
	expired := time.Now().Add(-24 * time.Hour)
	c.ExpiresAt = &expired
	seedCustomer(s, c)
	for i := 0; i < 121; i++ {
		r, p, b := rule, pool, binding
		r.ID, r.ListenPort, r.Paused, r.Deployed = fmt.Sprintf("paused-%03d", i), 20000+i, true, false
		p.ID, p.RuleID, p.Port = "pool-"+r.ID, r.ID, r.ListenPort
		b.Binding.ID, b.Binding.ForwardingRuleID, b.Binding.EndpointPoolID = "binding-"+r.ID, r.ID, p.ID
		s.forwardRules[r.ID], s.endpointPools[p.ID], s.vlessBindings[b.Binding.ID] = r, p, b
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		s.businessIdempotency["admin\x00forwarding.create\x00"+r.ID] = businessMutation{ResourceID: r.ID, Result: raw}
	}
	before, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	view := s.ForwardingReadView()
	for id := range s.forwardRules {
		got, err := view.ResolveForwardingConfiguration(context.Background(), "admin", id)
		if err != nil || got.Status != "paused" || !got.Paused || got.ServiceReady || got.LiveReadinessChecked || got.URI == "" {
			t.Fatal("paused cohort configuration failed", err)
		}
		if live, err := view.ResolveForwardingConnection(context.Background(), "admin", id); err == nil || live.URI != "" {
			t.Fatal("paused cohort gained a live connection")
		}
	}
	after, err := s.EncodeSnapshot()
	if err != nil || !bytes.Equal(before, after) || len(s.forwardRules) != 121 {
		t.Fatal("paused cohort read mutated persisted state")
	}
}

func TestConfigurationReadPreservesInactiveStateAndStableIdentity(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Store)
		status string
	}{
		{"configured", func(*Store) {}, "configured"},
		{"paused", func(s *Store) { r := s.forwardRules["rule"]; r.Paused = true; s.forwardRules[r.ID] = r }, "paused"},
		{"expired", func(s *Store) {
			c := s.customers["customer"]
			at := time.Now().Add(-time.Hour)
			c.ExpiresAt = &at
			seedCustomer(s, c)
		}, "inactive"},
		{"disabled", func(s *Store) { c := s.customers["customer"]; c.Disabled = true; s.customers[c.ID] = c }, "inactive"},
		{"quota", func(s *Store) {
			r := s.forwardRules["rule"]
			r.TrafficLimitBytes, r.TrafficUsedBytes = 1, 1
			s.forwardRules[r.ID] = r
		}, "inactive"},
		{"no live health", func(s *Store) { delete(s.protocolHealth, "pool") }, "configured"},
		{"no runtime or deployment", func(s *Store) {
			r := s.forwardRules["rule"]
			r.Paused, r.Deployed = true, false
			s.forwardRules[r.ID] = r
			clear(s.vlessRuntimeMaterials)
			clear(s.protocolHealth)
		}, "paused"},
		{"SOCKS5 landing", func(s *Store) {
			r := s.forwardRules["rule"]
			r.VLESSOutboundMode = forwarding.VLESSOutboundSOCKS5
			r.VLESSSOCKS5Username = "confidential-upstream-user"
			s.forwardRules[r.ID] = r
		}, "configured"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := connectionReadFixture(t)
			tc.mutate(s)
			before, err := s.EncodeSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			view := s.ForwardingReadView().WithOverlays(1, nil, nil, nil)
			var want vlessconnection.Configuration
			for _, source := range []vlessconnection.ConfigurationSource{s, view} {
				got, err := source.ResolveForwardingConfiguration(context.Background(), "admin", "rule")
				if err != nil {
					t.Fatal("saved configuration unavailable", err)
				}
				if got.Status != tc.status || got.ServiceReady || got.LiveReadinessChecked || got.Paused != s.forwardRules["rule"].Paused || got.BindingID != "binding" || got.RuleID != "rule" || got.CustomerID != "customer" || got.EndpointPoolID != "pool" || got.RuleRevision != 3 || got.BindingRevision != 1 {
					t.Fatal("invalid saved configuration metadata")
				}
				u, err := url.Parse(got.URI)
				if err != nil || u.Scheme != "vless" || u.User.Username() != s.vlessBindings["binding"].CredentialUUID || u.Host != "entry.example.test:443" || u.Query().Get("security") != "reality" || u.Query().Get("sni") != "example.org" || u.Query().Get("pbk") != s.forwardRules["rule"].RealityPublicKey {
					t.Fatal("configuration lost the saved client identity or Reality parameters")
				}
				if want.URI != "" && want != got {
					t.Fatal("view changed configuration")
				}
				want = got
			}
			if tc.name != "configured" && tc.name != "SOCKS5 landing" {
				if got, err := view.ResolveForwardingConnection(context.Background(), "admin", "rule"); err == nil || got.URI != "" {
					t.Fatal("saved export weakened the live rule gate")
				}
				if got, err := view.ResolveIdentityConnection(context.Background(), "admin", "binding"); err == nil || got.URI != "" {
					t.Fatal("saved export weakened the live identity gate")
				}
			}
			after, err := s.EncodeSnapshot()
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("configuration read mutated the store")
			}
		})
	}
}

func TestConfigurationReadFailsClosedForAmbiguousOwnershipAndMaterial(t *testing.T) {
	cases := map[string]func(*Store){
		"missing customer":     func(s *Store) { delete(s.customers, "customer") },
		"customer id mismatch": func(s *Store) { c := s.customers["customer"]; c.ID = "other"; s.customers["customer"] = c },
		"rule id mismatch":     func(s *Store) { r := s.forwardRules["rule"]; r.ID = "other"; s.forwardRules["rule"] = r },
		"customer authored":    func(s *Store) { clear(s.businessIdempotency) },
		"foreign pool":         func(s *Store) { p := s.endpointPools["pool"]; p.OwnerID = "other"; s.endpointPools[p.ID] = p },
		"missing pool":         func(s *Store) { delete(s.endpointPools, "pool") },
		"wrong pool id":        func(s *Store) { p := s.endpointPools["pool"]; p.ID = "other"; s.endpointPools["pool"] = p },
		"wrong pool rule":      func(s *Store) { p := s.endpointPools["pool"]; p.RuleID = "other"; s.endpointPools[p.ID] = p },
		"wrong pool group":     func(s *Store) { p := s.endpointPools["pool"]; p.GroupID = "other"; s.endpointPools[p.ID] = p },
		"wrong pool port":      func(s *Store) { p := s.endpointPools["pool"]; p.Port++; s.endpointPools[p.ID] = p },
		"wrong pool mode": func(s *Store) {
			p := s.endpointPools["pool"]
			p.Mode = endpoints.Mode("other")
			s.endpointPools[p.ID] = p
		},
		"wrong pool protocol": func(s *Store) { p := s.endpointPools["pool"]; p.Protocol = "tcp"; s.endpointPools[p.ID] = p },
		"invalid hostname": func(s *Store) {
			p := s.endpointPools["pool"]
			p.Hostname = "socks5://secret@host"
			s.endpointPools[p.ID] = p
		},
		"no binding": func(s *Store) { clear(s.vlessBindings) },
		"revoked binding": func(s *Store) {
			c := s.vlessBindings["binding"]
			c.Binding.State = vlessidentity.StateRevoked
			s.vlessBindings["binding"] = c
		},
		"invalid binding state": func(s *Store) {
			c := s.vlessBindings["binding"]
			c.Binding.State = "unknown"
			s.vlessBindings["binding"] = c
		},
		"binding id mismatch": func(s *Store) {
			c := s.vlessBindings["binding"]
			c.Binding.ID = "other"
			s.vlessBindings["binding"] = c
		},
		"binding customer mismatch": func(s *Store) {
			c := s.vlessBindings["binding"]
			c.Binding.CustomerID = "other"
			s.vlessBindings["binding"] = c
		},
		"invalid revision": func(s *Store) {
			c := s.vlessBindings["binding"]
			c.Binding.Revision = 0
			s.vlessBindings["binding"] = c
		},
		"invalid timestamps": func(s *Store) {
			c := s.vlessBindings["binding"]
			c.Binding.UpdatedAt = c.Binding.CreatedAt.Add(-time.Second)
			s.vlessBindings["binding"] = c
		},
		"active revocation": func(s *Store) {
			c := s.vlessBindings["binding"]
			at := time.Now()
			c.Binding.RevokedAt = &at
			s.vlessBindings["binding"] = c
		},
		"empty credential": func(s *Store) { c := s.vlessBindings["binding"]; c.CredentialUUID = ""; s.vlessBindings["binding"] = c },
		"invalid credential": func(s *Store) {
			c := s.vlessBindings["binding"]
			c.CredentialUUID = "credential-must-not-escape"
			s.vlessBindings["binding"] = c
		},
		"duplicate binding": func(s *Store) {
			c := s.vlessBindings["binding"]
			c.Binding.ID = "duplicate"
			s.vlessBindings["duplicate"] = c
		},
		"foreign duplicate binding": func(s *Store) {
			c := s.vlessBindings["binding"]
			c.Binding.ID, c.Binding.CustomerID, c.Binding.EndpointPoolID = "duplicate", "other", "foreign"
			s.vlessBindings["duplicate"] = c
		},
		"missing Reality destination": func(s *Store) { r := s.forwardRules["rule"]; r.RealityDestination = ""; s.forwardRules[r.ID] = r },
		"missing Reality public key":  func(s *Store) { r := s.forwardRules["rule"]; r.RealityPublicKey = ""; s.forwardRules[r.ID] = r },
		"invalid Reality public key":  func(s *Store) { r := s.forwardRules["rule"]; r.RealityPublicKey = "invalid"; s.forwardRules[r.ID] = r },
		"missing Reality SNI":         func(s *Store) { r := s.forwardRules["rule"]; r.RealityServerName = ""; s.forwardRules[r.ID] = r },
		"missing short id":            func(s *Store) { r := s.forwardRules["rule"]; r.RealityShortID = ""; s.forwardRules[r.ID] = r },
		"unsupported flow":            func(s *Store) { r := s.forwardRules["rule"]; r.VLESSFlow = "unsupported"; s.forwardRules[r.ID] = r },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s, _ := connectionReadFixture(t)
			mutate(s)
			for _, source := range []vlessconnection.ConfigurationSource{s, s.ForwardingReadView()} {
				got, err := source.ResolveForwardingConfiguration(context.Background(), "admin", "rule")
				if err == nil || !reflect.DeepEqual(got, vlessconnection.Configuration{}) {
					t.Fatal("invalid configuration escaped")
				}
			}
		})
	}
	s, _ := connectionReadFixture(t)
	if got, err := s.ResolveForwardingConfiguration(context.Background(), "foreign-admin", "rule"); !errors.Is(err, faults.ErrNotFound) || got.URI != "" {
		t.Fatal("foreign administrator received configuration")
	}
	if got, err := s.ResolveForwardingConfiguration(context.Background(), "", "rule"); !errors.Is(err, faults.ErrNotFound) || got.URI != "" {
		t.Fatal("empty administrator received configuration")
	}
}
