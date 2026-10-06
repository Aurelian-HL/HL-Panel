package scheduler

import (
	"errors"
	"testing"
)

func TestWeightedRoundRobinHonorsWeightsOverOneCycle(t *testing.T) {
	router, err := New(PolicyWeightedRoundRobin)
	if err != nil {
		t.Fatal(err)
	}
	candidates := []Candidate{
		{ID: "node-a", Weight: 1, Healthy: true},
		{ID: "node-b", Weight: 2, Healthy: true},
	}
	counts := map[string]int{}
	for i := 0; i < 3; i++ {
		selected, err := router.Select(candidates, "")
		if err != nil {
			t.Fatal(err)
		}
		counts[selected.ID]++
	}
	if counts["node-a"] != 1 || counts["node-b"] != 2 {
		t.Fatalf("weighted round robin counts = %#v, want node-a=1 node-b=2", counts)
	}
}

func TestWeightedLeastConnectionsUsesNormalizedLoad(t *testing.T) {
	router, err := New(PolicyWeightedLeastConnections)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := router.Select([]Candidate{
		{ID: "busy-high-weight", Weight: 10, ActiveConnections: 20, Healthy: true},
		{ID: "light-low-weight", Weight: 1, ActiveConnections: 3, Healthy: true},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if selected.ID != "busy-high-weight" {
		t.Fatalf("selected %q, want normalized load winner", selected.ID)
	}
}

func TestWeightedLeastConnectionsDoesNotOverflowCrossProduct(t *testing.T) {
	router, err := New(PolicyWeightedLeastConnections)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := router.Select([]Candidate{
		{ID: "busy-light", Weight: 1, ActiveConnections: 1 << 62, Healthy: true},
		{ID: "less-heavy", Weight: 2, ActiveConnections: (1 << 62) - 1, Healthy: true},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if selected.ID != "less-heavy" {
		t.Fatalf("selected = %#v, want less-heavy", selected)
	}
}

func TestRendezvousHashIsStableForKey(t *testing.T) {
	router, err := New(PolicyRendezvousHash)
	if err != nil {
		t.Fatal(err)
	}
	candidates := []Candidate{{ID: "a", Weight: 1, Healthy: true}, {ID: "b", Weight: 2, Healthy: true}}
	first, err := router.Select(candidates, "client-1")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		next, err := router.Select([]Candidate{candidates[1], candidates[0]}, "client-1")
		if err != nil {
			t.Fatal(err)
		}
		if next.ID != first.ID {
			t.Fatalf("selection changed from %q to %q", first.ID, next.ID)
		}
	}
}

func TestUnhealthyAndDrainingMembersAreNeverSelected(t *testing.T) {
	router, err := New(PolicyWeightedRoundRobin)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := router.Select([]Candidate{
		{ID: "offline", Weight: 100, Healthy: false},
		{ID: "draining", Weight: 100, Healthy: true, Draining: true},
		{ID: "ready", Weight: 1, Healthy: true},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if selected.ID != "ready" {
		t.Fatalf("selected %q, want ready", selected.ID)
	}
}

func TestZeroWeightMemberIsNotSelected(t *testing.T) {
	for _, policy := range []SelectionPolicy{PolicyWeightedRoundRobin, PolicyWeightedLeastConnections, PolicyRendezvousHash} {
		router, err := New(policy)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 20; i++ {
			selected, err := router.Select([]Candidate{
				{ID: "paused", Weight: 0, Healthy: true},
				{ID: "active", Weight: 1, Healthy: true},
			}, "client")
			if err != nil || selected.ID != "active" {
				t.Fatalf("policy %s: selected %q, error %v", policy, selected.ID, err)
			}
		}
		if _, err := router.Select([]Candidate{{ID: "paused", Weight: 0, Healthy: true}}, ""); !errors.Is(err, ErrNoHealthyMember) {
			t.Fatalf("policy %s: all zero weight: %v", policy, err)
		}
	}
}

func TestNoHealthyMemberAndInvalidCandidates(t *testing.T) {
	router, err := New(PolicyWeightedRoundRobin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := router.Select([]Candidate{{ID: "offline", Weight: 1}}, ""); !errors.Is(err, ErrNoHealthyMember) {
		t.Fatalf("error = %v, want ErrNoHealthyMember", err)
	}
	if _, err := router.Select([]Candidate{{ID: "", Weight: 1, Healthy: true}}, ""); !errors.Is(err, ErrInvalidCandidateSet) {
		t.Fatalf("error = %v, want ErrInvalidCandidateSet", err)
	}
}
