package memoryrepo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
)

func TestEndpointPoolProjectionMatchesAuthoritativeLookup(t *testing.T) {
	store := New(auth.Administrator{})
	store.endpointPools = map[string]endpoints.EndpointPool{
		"storage-key": {ID: "different-dto-id", RuleID: "unique"},
		"duplicate-1": {RuleID: "duplicate"},
		"duplicate-2": {RuleID: "duplicate"},
		"duplicate-3": {RuleID: "duplicate"},
		"other":       {RuleID: "other-rule"},
	}
	view := store.ForwardingReadView()
	if view.state.forwardingProjection.ruleEndpoints.entries != nil {
		t.Fatal("read publication eagerly built an unused endpoint index")
	}
	for _, test := range []struct {
		rule, id string
		err      error
	}{
		{"", "", faults.ErrValidation},
		{"missing", "", faults.ErrNotFound},
		{"unique", "storage-key", nil},
		{"duplicate", "", faults.ErrConflict},
		{"other-rule", "other", nil},
	} {
		t.Run(test.rule, func(t *testing.T) {
			for range 30 {
				wantID, wantErr := store.endpointPoolIDForRuleLocked(test.rule)
				gotID, gotErr := view.state.endpointPoolIDForRuleLocked(test.rule)
				if wantID != test.id || !errors.Is(wantErr, test.err) || gotID != wantID || !errors.Is(gotErr, test.err) {
					t.Fatalf("lookup mismatch: authoritative=(%q,%v), indexed=(%q,%v)", wantID, wantErr, gotID, gotErr)
				}
				if wantErr != nil && gotErr.Error() != wantErr.Error() {
					t.Fatalf("error changed: got %v, want %v", gotErr, wantErr)
				}
			}
		})
	}
	if store.forwardingProjection != nil {
		t.Fatal("authoritative store acquired a structural read cache")
	}
}

func TestEndpointPoolProjectionPublicationAndOverlayIsolation(t *testing.T) {
	store := New(auth.Administrator{})
	store.endpointPools["pool"] = endpoints.EndpointPool{ID: "pool", RuleID: "rule"}
	old := store.ForwardingReadView()
	cold := store.ForwardingReadView()
	for range 2 {
		if id, err := old.state.endpointPoolIDForRuleLocked("rule"); id != "pool" || err != nil {
			t.Fatal("initial pool not found")
		}
	}
	store.endpointPools["duplicate"] = endpoints.EndpointPool{ID: "duplicate", RuleID: "rule"}
	duplicateView := store.ForwardingReadView()
	if _, err := duplicateView.state.endpointPoolIDForRuleLocked("rule"); !errors.Is(err, faults.ErrConflict) {
		t.Fatal("new publication did not detect duplicate pool")
	}
	delete(store.endpointPools, "pool")
	delete(store.endpointPools, "duplicate")
	store.endpointPools["replacement"] = endpoints.EndpointPool{ID: "replacement", RuleID: "new-rule"}
	fresh := store.ForwardingReadView()
	if _, err := fresh.state.endpointPoolIDForRuleLocked("rule"); !errors.Is(err, faults.ErrNotFound) {
		t.Fatal("fresh publication retained deleted binding")
	}
	if id, err := fresh.state.endpointPoolIDForRuleLocked("new-rule"); id != "replacement" || err != nil {
		t.Fatal("fresh publication missed new binding")
	}
	for _, view := range []*ForwardingReadView{old, cold, old.WithOverlays(2, nil, nil, nil), cold.WithOverlays(2, nil, nil, nil)} {
		if id, err := view.state.endpointPoolIDForRuleLocked("rule"); id != "pool" || err != nil {
			t.Fatal("source mutation changed an older view or its overlay")
		}
	}
	if _, err := duplicateView.WithOverlays(3, nil, nil, nil).state.endpointPoolIDForRuleLocked("rule"); !errors.Is(err, faults.ErrConflict) {
		t.Fatal("source repair incorrectly changed an older duplicate view")
	}
}

func TestEndpointPoolProjectionConcurrentFirstLookup(t *testing.T) {
	store := New(auth.Administrator{})
	for i := range 200 {
		store.endpointPools[fmt.Sprintf("pool-%d", i)] = endpoints.EndpointPool{RuleID: fmt.Sprintf("rule-%d", i)}
	}
	store.endpointPools["duplicate"] = endpoints.EndpointPool{RuleID: "rule-0"}
	view := store.ForwardingReadView()
	start := make(chan struct{})
	errs := make(chan error, 32)
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			local := view.WithOverlays(2, nil, nil, nil)
			for i := range 200 {
				id, err := local.state.endpointPoolIDForRuleLocked(fmt.Sprintf("rule-%d", i))
				if i == 0 && errors.Is(err, faults.ErrConflict) {
					continue
				}
				if i == 0 || err != nil || id != fmt.Sprintf("pool-%d", i) {
					errs <- fmt.Errorf("lookup %d returned %q, %v", i, id, err)
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestEndpointPoolProjectionSingleProofAvoidsIndexAllocation(t *testing.T) {
	store, observation := protocolPublicationFixture(t)
	view := store.ForwardingReadView()
	merged := view.WithOverlays(2, nil, []gatewaymembership.ProtocolObservation{observation}, nil)
	index := &view.state.forwardingProjection.ruleEndpoints
	if index.entries != nil || merged.state.protocolHealth["pool"]["node"] != observation {
		t.Fatal("single proof did not use allocation-free first lookup")
	}
	if id, err := merged.state.endpointPoolIDForRuleLocked("rule"); id != "pool" || err != nil || index.entries == nil {
		t.Fatal("repeated structural lookup did not build the shared index")
	}
}

func TestEndpointPoolProjectionRejectsDuplicateHealthProofs(t *testing.T) {
	store, observation := protocolPublicationFixture(t)
	duplicate := store.endpointPools["pool"]
	duplicate.ID = "duplicate"
	store.endpointPools["duplicate"] = duplicate
	view := store.ForwardingReadView()
	for range 30 {
		merged := view.WithOverlays(2, nil, []gatewaymembership.ProtocolObservation{observation}, nil)
		results, changed, _, err := merged.CheckProtocolHealthRenewal(context.Background(), []gatewaymembership.ProtocolObservation{observation})
		if err != nil || len(results) != 1 || !errors.Is(results[0], faults.ErrConflict) || changed {
			t.Fatalf("duplicate proof accepted: results=%v changed=%v err=%v", results, changed, err)
		}
		if len(merged.state.protocolHealth["pool"]) != 0 || len(merged.state.protocolHealth["duplicate"]) != 0 ||
			merged.state.endpointMembers["pool"]["node"].LastHealthAt != nil {
			t.Fatal("duplicate overlay changed protocol health or lease")
		}
	}
}

func TestEndpointPoolProjectionPreservesProofValidation(t *testing.T) {
	for name, change := range map[string]func(*gatewaymembership.ProtocolObservation){
		"valid":            func(*gatewaymembership.ProtocolObservation) {},
		"old revision":     func(o *gatewaymembership.ProtocolObservation) { o.RuleRevision-- },
		"wrong generation": func(o *gatewaymembership.ProtocolObservation) { o.NodeConfigGeneration++ },
		"wrong hash":       func(o *gatewaymembership.ProtocolObservation) { o.ConfigSHA256 = strings.Repeat("a", 64) },
		"wrong host":       func(o *gatewaymembership.ProtocolObservation) { o.DialHost = "other.example.test" },
		"extended lease": func(o *gatewaymembership.ProtocolObservation) {
			o.LeaseExpiresAt = o.VerifiedAt.Add(2 * endpoints.DefaultHealthTTL)
		},
		"expired": func(o *gatewaymembership.ProtocolObservation) {
			o.VerifiedAt = time.Now().UTC().Add(-2 * endpoints.DefaultHealthTTL)
			o.LeaseExpiresAt = o.VerifiedAt.Add(endpoints.DefaultHealthTTL)
		},
	} {
		t.Run(name, func(t *testing.T) {
			store, observation := protocolPublicationFixture(t)
			change(&observation)
			view := store.ForwardingReadView()
			results, changed, _, err := view.CheckProtocolHealthRenewal(context.Background(), []gatewaymembership.ProtocolObservation{observation})
			wantErr := store.PublishProtocolHealth(context.Background(), observation)
			if err != nil || len(results) != 1 || !errors.Is(results[0], wantErr) || changed != (wantErr == nil) {
				t.Fatalf("proof validation changed: results=%v changed=%v err=%v want=%v", results, changed, err, wantErr)
			}
			if (name == "valid") != (wantErr == nil) {
				t.Fatalf("fixture did not exercise expected proof boundary: %v", wantErr)
			}
			if len(view.state.protocolHealth["pool"]) != 0 || view.state.endpointMembers["pool"]["node"].LastHealthAt != nil {
				t.Fatal("validation mutated its immutable input")
			}
		})
	}
}

// Same-process paired benchmark: only the indexed resolver differs. Fixtures
// and view publication are outside the timer; the complete overlay clone and
// merge remain inside. The cold variant includes a new index per request.
func BenchmarkProtocolHealthEndpointPoolMerge(b *testing.B) {
	for _, fixture := range []struct{ count, batch int }{{100, 100}, {500, 500}, {2000, 2000}, {2000, 1}, {2000, 47}} {
		count, batch := fixture.count, fixture.batch
		label := fmt.Sprintf("pools_%d", count)
		if batch != count {
			label += fmt.Sprintf("_batch_%d", batch)
		}
		for _, mode := range []string{"scan", "indexed_hot", "indexed_cold"} {
			b.Run(label+"/"+mode, func(b *testing.B) {
				store := New(auth.Administrator{})
				observations := make([]gatewaymembership.ProtocolObservation, batch)
				verified := time.Now().UTC().Add(-time.Second)
				for i := range count {
					poolID, ruleID := fmt.Sprintf("pool-%d", i), fmt.Sprintf("rule-%d", i)
					store.endpointPools[poolID] = endpoints.EndpointPool{ID: poolID, RuleID: ruleID}
					store.endpointMembers[poolID] = map[string]endpoints.EndpointPoolMember{"node": {PoolID: poolID, NodeID: "node"}}
					if i < batch {
						observations[i] = gatewaymembership.ProtocolObservation{RuleID: ruleID, NodeID: "node", VerifiedAt: verified, LeaseExpiresAt: verified.Add(endpoints.DefaultHealthTTL)}
					}
				}
				view := store.ForwardingReadView()
				if mode == "scan" {
					view.state.forwardingProjection = nil
				} else if mode == "indexed_hot" {
					for range 2 {
						if _, err := view.state.endpointPoolIDForRuleLocked("rule-0"); err != nil {
							b.Fatal(err)
						}
					}
				}
				var merged *ForwardingReadView
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if mode == "indexed_cold" {
						view.state.forwardingProjection = &forwardingProjection{}
					}
					merged = view.WithOverlays(2, nil, observations, nil)
				}
				b.StopTimer()
				for i, observation := range observations {
					poolID := fmt.Sprintf("pool-%d", i)
					member := merged.state.endpointMembers[poolID]["node"]
					if merged.state.protocolHealth[poolID]["node"] != observation || member.LastHealthAt == nil || !member.LastHealthAt.Equal(verified) {
						b.Fatal("merge failed to restore committed proof")
					}
					if len(view.state.protocolHealth[poolID]) != 0 || view.state.endpointMembers[poolID]["node"].LastHealthAt != nil {
						b.Fatal("merge changed base view")
					}
				}
				for i := batch; i < count; i++ {
					poolID := fmt.Sprintf("pool-%d", i)
					if len(merged.state.protocolHealth[poolID]) != 0 || merged.state.endpointMembers[poolID]["node"].LastHealthAt != nil {
						b.Fatal("merge changed an unobserved pool")
					}
				}
			})
		}
	}
}
