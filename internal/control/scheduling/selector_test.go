package scheduling

import (
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/endpoints"
)

func TestSelectorFiltersHealthAndPriorityBeforePolicy(t *testing.T) {
	now := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	recent := now.Add(-time.Second)
	stale := now.Add(-time.Hour)
	candidates := []endpoints.EndpointPoolMember{
		{NodeID: "high-priority", Priority: 10, Weight: 100, State: endpoints.CandidateEligible, LastHealthAt: &recent},
		{NodeID: "preferred", Priority: 0, Weight: 1, State: endpoints.CandidateEligible, LastHealthAt: &recent},
		{NodeID: "stale", Priority: 0, Weight: 1000, State: endpoints.CandidateEligible, LastHealthAt: &stale},
		{NodeID: "draining", Priority: 0, Weight: 1000, State: endpoints.CandidateDraining, LastHealthAt: &recent},
	}
	selector := NewSelector()
	for _, policy := range []endpoints.SelectionPolicy{
		endpoints.SelectionWeightedRoundRobin,
		endpoints.SelectionWeightedLeastConnections,
		endpoints.SelectionRendezvousHash,
	} {
		selected, err := selector.Select("pool-1", policy, candidates, "client-key", now, time.Minute)
		if err != nil {
			t.Fatalf("%s selection failed: %v", policy, err)
		}
		if selected.NodeID != "preferred" {
			t.Fatalf("%s selected %q despite priority/health filtering", policy, selected.NodeID)
		}
	}
}

func TestWeightedLeastConnectionsUsesWeightAndStableTieBreak(t *testing.T) {
	now := time.Now().UTC()
	candidates := []endpoints.EndpointPoolMember{
		{NodeID: "z", Weight: 100, ActiveConnections: 10, State: endpoints.CandidateEligible, LastHealthAt: &now},
		{NodeID: "a", Weight: 100, ActiveConnections: 10, State: endpoints.CandidateEligible, LastHealthAt: &now},
	}
	selected, err := NewSelector().Select("pool-1", endpoints.SelectionWeightedLeastConnections, candidates, "", now, time.Minute)
	if err != nil {
		t.Fatalf("selection failed: %v", err)
	}
	if selected.NodeID != "a" {
		t.Fatalf("tie did not use stable node id, selected %q", selected.NodeID)
	}
}

func TestSelectorReturnsNoEligibleCandidates(t *testing.T) {
	now := time.Now().UTC()
	_, err := NewSelector().Select("pool-1", endpoints.SelectionWeightedRoundRobin, []endpoints.EndpointPoolMember{{
		NodeID: "offline", Weight: 100, State: endpoints.CandidateQuarantined, LastHealthAt: &now,
	}}, "", now, time.Minute)
	if err != ErrNoEligibleCandidates {
		t.Fatalf("error = %v, want ErrNoEligibleCandidates", err)
	}
}
