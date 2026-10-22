package memoryrepo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
)

// Only structural import evidence is populated. This measures publication of
// the accounting view, not decoding, config copies, PostgreSQL or end-to-end apply.
func usageImportFixture(count int, administrator bool) *Store {
	s := New(auth.Administrator{ID: "usage-admin", Username: "usage-admin"})
	now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("usage-rule-%04d", i)
		groupID, poolID := "group-"+id, "pool-"+id
		r := forwarding.Rule{ID: id, CustomerID: "klunassigned_" + groupID,
			EntryGroupID: groupID, ListenPort: 12000 + i, OwnerKind: forwarding.OwnerUnassigned,
			VLESSOutboundMode: forwarding.VLESSOutboundNative,
			ImportedTraffic:   forwarding.ImportedTraffic{SourceID: "synthetic-source", BatchID: "synthetic-batch", LineID: int64(i + 1), ObservedAt: now}}
		if administrator {
			r.OwnerKind, r.OwnerID = forwarding.OwnerAdministrator, "usage-admin"
			r.CustomerID = "imported-subject-" + id
		}
		s.forwardRules[id] = r
		s.endpointPools[poolID] = endpoints.EndpointPool{ID: poolID, RuleID: id, OwnerID: "usage-admin", GroupID: groupID, Port: r.ListenPort}
		s.vlessBindings[id] = vlessidentity.CredentialRecord{Binding: vlessidentity.Binding{ID: id,
			ForwardingRuleID: id, CustomerID: r.CustomerID, EndpointPoolID: poolID,
			ImportedFrom: &vlessidentity.ImportOrigin{BatchID: r.ImportedTraffic.BatchID, LineID: r.ImportedTraffic.LineID}}}
	}
	return s
}

func TestUsageImportedSubjectsMatchAuthoritativeValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		change  func(*Store)
		allowed bool
	}{
		{"administrator_import", func(*Store) {}, true},
		{"missing_administrator", func(s *Store) { delete(s.adminsByUsername, "usage-admin") }, false},
		{"missing_baseline", func(s *Store) {
			r := s.forwardRules["usage-rule-0000"]
			r.ImportedTraffic = forwarding.ImportedTraffic{}
			s.forwardRules[r.ID] = r
		}, false},
		{"invalid_baseline", func(s *Store) {
			r := s.forwardRules["usage-rule-0000"]
			r.ImportedTraffic.UploadBytes = -1
			s.forwardRules[r.ID] = r
		}, false},
		{"wrong_rule_owner", func(s *Store) {
			r := s.forwardRules["usage-rule-0000"]
			r.OwnerID = "another-admin"
			s.forwardRules[r.ID] = r
		}, false},
		{"wrong_pool_owner", func(s *Store) {
			p := s.endpointPools["pool-usage-rule-0000"]
			p.OwnerID = "another-admin"
			s.endpointPools[p.ID] = p
		}, false},
		{"wrong_pool_rule", func(s *Store) {
			p := s.endpointPools["pool-usage-rule-0000"]
			p.RuleID = "another-rule"
			s.endpointPools[p.ID] = p
		}, false},
		{"wrong_pool_group", func(s *Store) {
			p := s.endpointPools["pool-usage-rule-0000"]
			p.GroupID = "another-group"
			s.endpointPools[p.ID] = p
		}, false},
		{"wrong_pool_port", func(s *Store) { p := s.endpointPools["pool-usage-rule-0000"]; p.Port++; s.endpointPools[p.ID] = p }, false},
		{"wrong_pool_identity", func(s *Store) {
			p := s.endpointPools["pool-usage-rule-0000"]
			p.ID = "another-pool"
			s.endpointPools["pool-usage-rule-0000"] = p
		}, false},
		{"wrong_binding_pool", func(s *Store) {
			b := s.vlessBindings["usage-rule-0000"]
			b.Binding.EndpointPoolID = "missing"
			s.vlessBindings[b.Binding.ID] = b
		}, false},
		{"wrong_binding_rule", func(s *Store) {
			b := s.vlessBindings["usage-rule-0000"]
			b.Binding.ForwardingRuleID = "missing"
			s.vlessBindings[b.Binding.ID] = b
		}, false},
		{"wrong_binding_subject", func(s *Store) {
			b := s.vlessBindings["usage-rule-0000"]
			b.Binding.CustomerID = "another-subject"
			s.vlessBindings[b.Binding.ID] = b
		}, false},
		{"missing_origin", func(s *Store) {
			b := s.vlessBindings["usage-rule-0000"]
			b.Binding.ImportedFrom = nil
			s.vlessBindings[b.Binding.ID] = b
		}, false},
		{"wrong_origin_batch", func(s *Store) { s.vlessBindings["usage-rule-0000"].Binding.ImportedFrom.BatchID = "another-batch" }, false},
		{"wrong_origin_line", func(s *Store) { s.vlessBindings["usage-rule-0000"].Binding.ImportedFrom.LineID++ }, false},
		{"subject_too_long", func(s *Store) {
			r := s.forwardRules["usage-rule-0000"]
			r.CustomerID = strings.Repeat("x", 129)
			s.forwardRules[r.ID] = r
			b := s.vlessBindings[r.ID]
			b.Binding.CustomerID = r.CustomerID
			s.vlessBindings[r.ID] = b
		}, false},
		{"standard_subject_without_import", func(s *Store) {
			r := s.forwardRules["usage-rule-0000"]
			r.CustomerID = forwarding.AdministratorSubjectID(r.OwnerID)
			r.ImportedTraffic = forwarding.ImportedTraffic{}
			s.forwardRules[r.ID] = r
		}, false},
		{"standard_subject_with_import", func(s *Store) {
			r := s.forwardRules["usage-rule-0000"]
			r.CustomerID = forwarding.AdministratorSubjectID(r.OwnerID)
			s.forwardRules[r.ID] = r
			b := s.vlessBindings[r.ID]
			b.Binding.CustomerID = r.CustomerID
			s.vlessBindings[r.ID] = b
		}, true},
		{"customer_import_is_not_unassigned", func(s *Store) {
			r := s.forwardRules["usage-rule-0000"]
			r.OwnerKind, r.OwnerID = forwarding.OwnerCustomer, r.CustomerID
			s.forwardRules[r.ID] = r
		}, false},
		{"unassigned_without_administrator", func(s *Store) {
			r := s.forwardRules["usage-rule-0000"]
			r.OwnerKind, r.OwnerID = forwarding.OwnerUnassigned, ""
			r.CustomerID = "klunassigned_" + r.EntryGroupID
			s.forwardRules[r.ID] = r
			delete(s.adminsByUsername, "usage-admin")
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := usageImportFixture(1, true)
			tc.change(s)
			subject := s.forwardRules["usage-rule-0000"].CustomerID
			for name, validate := range map[string]func(context.Context, string) error{
				"authoritative":         s.ValidateUnassignedSubject,
				"detached":              s.UsageReadView().ValidateUnassignedSubject,
				"forwarding_projection": s.ForwardingReadView().state.ValidateUnassignedSubject,
			} {
				err := validate(context.Background(), subject)
				if tc.allowed && err != nil || !tc.allowed && !errors.Is(err, faults.ErrNotFound) {
					t.Fatalf("%s allowed=%v: %v", name, tc.allowed, err)
				}
			}
		})
	}
}

func TestUsageImportedSubjectRevocationDoesNotChangePublishedView(t *testing.T) {
	s := usageImportFixture(1, true)
	subject := s.forwardRules["usage-rule-0000"].CustomerID
	view := s.UsageReadView()
	delete(s.adminsByUsername, "usage-admin")
	s.vlessBindings["usage-rule-0000"].Binding.ImportedFrom.BatchID = "revoked-origin"
	if err := s.UsageReadView().ValidateUnassignedSubject(context.Background(), subject); !errors.Is(err, faults.ErrNotFound) {
		t.Fatal("fresh view retained revoked subject", err)
	}
	if err := view.ValidateUnassignedSubject(context.Background(), subject); err != nil {
		t.Fatal("published view changed with source", err)
	}
}

func BenchmarkUsageImportPublication(b *testing.B) {
	for _, administrator := range []bool{true, false} {
		for _, count := range []int{100, 500, 2000} {
			b.Run(fmt.Sprintf("administrator_%v/rules_%d", administrator, count), func(b *testing.B) {
				s := usageImportFixture(count, administrator)
				if got := len(s.UsageReadView().unassigned); got != count {
					b.Fatalf("subjects=%d want=%d", got, count)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if len(s.UsageReadView().unassigned) != count {
						b.Fatal("lost subjects")
					}
				}
			})
		}
	}
}

// Frozen pre-change constructor for paired measurements in the same process.
// This reference deliberately retains the repeated provenance scans.
func usageReadViewBeforeImportIndex(s *Store) *UsageReadView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v := &UsageReadView{
		unassigned: make(map[string]bool),
		customers:  make(map[string]customers.Customer, len(s.customers)),
		rules:      make(map[string]forwarding.RuleTrafficState, len(s.forwardRules)),
		configs:    make(map[string]map[int64]deploymentreceipts.HistoricalInput, len(s.nodeConfigsByNode)),
	}
	for id, c := range s.customers {
		v.customers[id] = s.customerWithLoginLocked(c)
	}
	for id, r := range s.forwardRules {
		v.rules[id] = forwarding.TrafficState(r)
		if r.IsUnassignedImport() || r.OwnerKind == forwarding.OwnerAdministrator && s.importedRuleManagedByLocked(r, r.OwnerID) && s.hasAdministratorSubjectLocked(r) {
			v.unassigned[r.CustomerID] = true
		}
	}
	for nodeID, configs := range s.nodeConfigsByNode {
		v.configs[nodeID] = make(map[int64]deploymentreceipts.HistoricalInput, len(configs))
		for generation, c := range configs {
			h := NodeConfigHistory{c, s.applyResultsByNode[nodeID][generation], s.applyAttemptsByNode[nodeID][generation]}
			v.configs[nodeID][generation] = h.UsageInput()
		}
	}
	return v
}

func BenchmarkUsageImportPublicationPaired(b *testing.B) {
	for _, administrator := range []bool{true, false} {
		for _, count := range []int{100, 500, 2000} {
			s := usageImportFixture(count, administrator)
			b.Run(fmt.Sprintf("administrator_%v/rules_%d", administrator, count), func(b *testing.B) {
				for _, build := range []struct {
					name string
					view func(*Store) *UsageReadView
				}{
					{"before", usageReadViewBeforeImportIndex}, {"after", (*Store).UsageReadView},
				} {
					b.Run(build.name, func(b *testing.B) {
						b.ReportAllocs()
						for i := 0; i < b.N; i++ {
							if len(build.view(s).unassigned) != count {
								b.Fatal("lost subjects")
							}
						}
					})
				}
			})
		}
	}
}
