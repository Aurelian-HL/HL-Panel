package memoryrepo

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
)

// Freeze the former renewal algorithm for same-process comparisons. The full
// frozen full copy remains the independent baseline for validation ordering,
// per-observation errors, and activation fallback, as well as allocations.
func fullCopyProtocolRenewal(ctx context.Context, view *ForwardingReadView, observations []gatewaymembership.ProtocolObservation) (results []error, changed, activation bool, err error) {
	if err = ctx.Err(); err != nil {
		return nil, false, false, err
	}
	state := fullCopyForwardingOverlays(view, view.state.persistenceRevision, nil, nil, nil).state
	for _, observation := range observations {
		if err = ctx.Err(); err != nil {
			return nil, false, false, err
		}
		if rule, exists := state.forwardRules[observation.RuleID]; exists && !rule.Deployed {
			activation = true
		}
	}
	results = make([]error, len(observations))
	for i, observation := range observations {
		if err = ctx.Err(); err != nil {
			return nil, false, false, err
		}
		results[i] = state.PublishProtocolHealth(ctx, observation)
		changed = changed || results[i] == nil
	}
	return results, changed, activation, nil
}

func sortedRenewalProofs(proofs []gatewaymembership.ProtocolObservation) {
	sort.Slice(proofs, func(i, j int) bool {
		if proofs[i].RuleID == proofs[j].RuleID {
			return proofs[i].NodeID < proofs[j].NodeID
		}
		return proofs[i].RuleID < proofs[j].RuleID
	})
}

func TestProtocolHealthRenewalScopedCopyMatchesFullCopy(t *testing.T) {
	for _, scenario := range []string{"empty", "accepted and rejected", "first activation", "all rejected", "duplicate pools"} {
		t.Run(scenario, func(t *testing.T) {
			view, proofs := sizedProtocolRenewalFixture(t, 6, 3)
			sortedRenewalProofs(proofs)
			previous := append([]gatewaymembership.ProtocolObservation(nil), proofs...)
			for i := range previous {
				previous[i].VerifiedAt = previous[i].VerifiedAt.Add(-2 * time.Second)
				previous[i].LeaseExpiresAt = previous[i].LeaseExpiresAt.Add(-2 * time.Second)
			}
			view = view.WithOverlays(7, nil, previous, nil)
			newer, invalid, missing := proofs[0], proofs[1], proofs[2]
			newer.VerifiedAt = newer.VerifiedAt.Add(100 * time.Millisecond)
			newer.LeaseExpiresAt = newer.LeaseExpiresAt.Add(100 * time.Millisecond)
			invalid.RuleRevision--
			missing.RuleID = "missing"
			batch := []gatewaymembership.ProtocolObservation{proofs[0], proofs[0], previous[0], invalid, proofs[1], newer, proofs[0], missing, proofs[3]}
			switch scenario {
			case "empty":
				batch = nil
			case "first activation":
				view.state.forwardRules = maps.Clone(view.state.forwardRules)
				r := view.state.forwardRules[proofs[0].RuleID]
				r.Deployed = false
				view.state.forwardRules[r.ID] = r
			case "all rejected":
				batch = []gatewaymembership.ProtocolObservation{invalid, previous[0], missing, {}}
			case "duplicate pools":
				view.state.endpointPools = maps.Clone(view.state.endpointPools)
				p := view.state.endpointPools["pool-"+proofs[0].RuleID]
				p.ID = "duplicate"
				view.state.endpointPools[p.ID] = p
				// The fixture's structure was changed before publication/use.
				view.state.forwardingProjection = &forwardingProjection{}
			}
			members := cloneViewMap(view.state.endpointMembers, func(m map[string]endpoints.EndpointPoolMember) map[string]endpoints.EndpointPoolMember {
				return cloneViewMap(m, cloneEndpointMember)
			})
			health := cloneViewMap(view.state.protocolHealth, func(m map[string]gatewaymembership.ProtocolObservation) map[string]gatewaymembership.ProtocolObservation {
				return maps.Clone(m)
			})
			rules := cloneViewMap(view.state.forwardRules, cloneForwardingRule)
			revision := view.state.gatewayMutationRevision
			want, wantChanged, wantActivation, wantErr := fullCopyProtocolRenewal(context.Background(), view, batch)
			got, changed, activation, err := view.CheckProtocolHealthRenewal(context.Background(), batch)
			if err != wantErr || changed != wantChanged || activation != wantActivation || len(got) != len(want) {
				t.Fatalf("result differs: %v %v %v %v; want %v %v %v %v", got, changed, activation, err, want, wantChanged, wantActivation, wantErr)
			}
			for i := range got {
				if fmt.Sprint(got[i]) != fmt.Sprint(want[i]) {
					t.Fatalf("observation %d error differs: %v, want %v", i, got[i], want[i])
				}
			}
			if !reflect.DeepEqual(members, view.state.endpointMembers) || !reflect.DeepEqual(health, view.state.protocolHealth) || !reflect.DeepEqual(rules, view.state.forwardRules) || revision != view.state.gatewayMutationRevision {
				t.Fatal("scratch renewal leaked health, lease, activation or revision into its source")
			}
		})
	}
}

type cancelRenewalAfterChecks struct {
	context.Context
	checks int
}

func (ctx *cancelRenewalAfterChecks) Err() error {
	ctx.checks++
	// Initial check, two activation checks, first proof, then cancellation.
	if ctx.checks >= 5 {
		return context.Canceled
	}
	return nil
}

func TestProtocolHealthRenewalScopedCopyCancellationAfterAcceptedProof(t *testing.T) {
	view, proofs := sizedProtocolRenewalFixture(t, 4, 2)
	ctx := &cancelRenewalAfterChecks{Context: context.Background()}
	results, changed, activation, err := view.CheckProtocolHealthRenewal(ctx, proofs[:2])
	if err != context.Canceled || results != nil || changed || activation || ctx.checks != 5 {
		t.Fatalf("cancellation semantics changed: %v %v %v %v (%d checks)", results, changed, activation, err, ctx.checks)
	}
	for poolID, members := range view.state.endpointMembers {
		if len(view.state.protocolHealth[poolID]) != 0 {
			t.Fatal("cancelled partial renewal leaked a proof")
		}
		for _, member := range members {
			if member.LastHealthAt != nil {
				t.Fatal("cancelled partial renewal leaked a lease")
			}
		}
	}
}

func TestProtocolHealthRenewalScopedCopyConcurrentOverlays(t *testing.T) {
	view, proofs := scaledProtocolRenewalFixture(t)
	sortedRenewalProofs(proofs)
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := view.WithOverlays(8, nil, proofs[:28], nil)
			before := local.state.protocolHealth["pool-"+proofs[0].RuleID][proofs[0].NodeID]
			batch := append([]gatewaymembership.ProtocolObservation(nil), proofs[:32]...)
			for i := range batch {
				batch[i].VerifiedAt = batch[i].VerifiedAt.Add(100 * time.Millisecond)
				batch[i].LeaseExpiresAt = batch[i].LeaseExpiresAt.Add(100 * time.Millisecond)
			}
			results, changed, activation, err := local.CheckProtocolHealthRenewal(context.Background(), batch)
			if err != nil || !changed || activation {
				t.Error("concurrent renewal failed", err, changed, activation)
			}
			for _, result := range results {
				if result != nil {
					t.Error("concurrent proof rejected", result)
				}
			}
			if after := local.state.protocolHealth["pool-"+proofs[0].RuleID][proofs[0].NodeID]; after != before {
				t.Error("renewal modified its overlay's previous proof")
			}
		}()
	}
	wg.Wait()
	for poolID, members := range view.state.endpointMembers {
		if len(view.state.protocolHealth[poolID]) != 0 {
			t.Fatal("concurrent overlays leaked proof into base")
		}
		for _, member := range members {
			if member.LastHealthAt != nil {
				t.Fatal("concurrent overlays leaked lease into base")
			}
		}
	}
}

func BenchmarkProtocolHealthRenewalCopy(b *testing.B) {
	for _, scenario := range []struct {
		name                string
		rules, nodes, batch int
		reject              bool
	}{
		{"empty", 42, 28, 0, false},
		{"single", 42, 28, 1, false},
		{"same_pool_28", 42, 28, 28, false},
		{"batch_32", 42, 28, 32, false},
		{"all_1176", 42, 28, 1176, false},
		{"rejected_32", 42, 28, 32, true},
		{"pools_500_single", 500, 1, 1, false},
	} {
		b.Run(scenario.name, func(b *testing.B) {
			view, proofs := sizedProtocolRenewalFixture(b, scenario.rules, scenario.nodes)
			sortedRenewalProofs(proofs)
			// Persisted old health must also be copied by the baseline, not just
			// empty health maps. All proofs and fixtures are synthetic loopback inputs.
			old := append([]gatewaymembership.ProtocolObservation(nil), proofs...)
			for i := range old {
				old[i].VerifiedAt = old[i].VerifiedAt.Add(-time.Second)
				old[i].LeaseExpiresAt = old[i].LeaseExpiresAt.Add(-time.Second)
			}
			view = view.WithOverlays(9, nil, old, nil)
			batch := proofs[:scenario.batch]
			if scenario.reject {
				batch = append([]gatewaymembership.ProtocolObservation(nil), batch...)
				for i := range batch {
					batch[i].RuleRevision--
				}
			}
			for _, mode := range []string{"full_copy", "scoped_copy"} {
				check := fullCopyProtocolRenewal
				if mode == "scoped_copy" {
					check = func(ctx context.Context, v *ForwardingReadView, p []gatewaymembership.ProtocolObservation) ([]error, bool, bool, error) {
						return v.CheckProtocolHealthRenewal(ctx, p)
					}
				}
				b.Run(mode, func(b *testing.B) {
					// Equal warm evaluator and structural-index state in both modes.
					check(context.Background(), view, proofs[:1])
					check(context.Background(), view, proofs[:1])
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						results, changed, activation, err := check(context.Background(), view, batch)
						if err != nil || activation || changed != (len(batch) > 0 && !scenario.reject) || len(results) != len(batch) {
							b.Fatalf("renewal failed: %v %v %v", err, changed, activation)
						}
						for _, result := range results {
							if (result != nil) != scenario.reject {
								b.Fatal("unexpected proof result", result)
							}
						}
					}
					b.StopTimer()
					for i, proof := range old {
						poolID := "pool-" + proofs[i].RuleID
						member := view.state.endpointMembers[poolID][proof.NodeID]
						if view.state.protocolHealth[poolID][proof.NodeID] != proof || member.LastHealthAt == nil || !member.LastHealthAt.Equal(proof.VerifiedAt) {
							b.Fatal("renewal changed immutable proof or lease")
						}
					}
				})
			}
		})
	}
}
