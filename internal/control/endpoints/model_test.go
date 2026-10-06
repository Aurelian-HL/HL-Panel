package endpoints

import (
	"testing"
	"time"
)

func TestSingleEndpointCandidateEligibility(t *testing.T) {
	now := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	recent := now.Add(-10 * time.Second)
	base := EndpointPoolMember{
		NodeID:       "node-1",
		Weight:       100,
		State:        CandidateEligible,
		LastHealthAt: &recent,
	}
	if !base.CandidateEligibleForNewConnection(now, time.Minute) {
		t.Fatal("recent eligible member was rejected")
	}
	for name, member := range map[string]EndpointPoolMember{
		"draining":   withState(base, CandidateDraining),
		"quarantine": withState(base, CandidateQuarantined),
		"disabled":   withState(base, CandidateDisabled),
		"stale":      withHealth(base, now.Add(-2*time.Minute)),
		"future":     withHealth(base, now.Add(time.Second)),
	} {
		t.Run(name, func(t *testing.T) {
			if member.CandidateEligibleForNewConnection(now, time.Minute) {
				t.Fatalf("member %q was incorrectly accepted", name)
			}
		})
	}
}

func TestEndpointPoolValidationRejectsSubscriptionPoolAndDuplicates(t *testing.T) {
	service := NewService(nil, nil)
	pool := EndpointPool{
		ID:              "pool-1",
		Name:            "public-vless",
		GroupID:         "group-1",
		Mode:            ModeSingleServiceEndpoint,
		Protocol:        "vless",
		Hostname:        "2.hongle.work",
		Port:            443,
		SelectionPolicy: SelectionWeightedLeastConnections,
	}
	members := []EndpointPoolMember{{GroupID: "group-1", NodeID: "node-1", Weight: 100, State: CandidateEligible}, {GroupID: "group-1", NodeID: "node-1", Weight: 100, State: CandidateEligible}}
	if err := service.ValidatePool(pool, members); err == nil {
		t.Fatal("duplicate endpoint candidates were accepted")
	}
	pool.Mode = Mode("SUBSCRIPTION_POOL")
	if err := service.ValidatePool(pool, []EndpointPoolMember{{GroupID: "group-1", NodeID: "node-1", Weight: 100, State: CandidateEligible}}); err == nil {
		t.Fatal("subscription pool mode was accepted")
	}
}

func withState(member EndpointPoolMember, state CandidateState) EndpointPoolMember {
	member.State = state
	return member
}

func withHealth(member EndpointPoolMember, health time.Time) EndpointPoolMember {
	member.LastHealthAt = &health
	return member
}
