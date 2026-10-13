package endpointrouter

import (
	"errors"
	"testing"
	"time"
)

func TestProbeVetoSurvivesDialCooldownAndContinuousRenewal(t *testing.T) {
	router, err := New(SelectionPolicyWeightedRoundRobin)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	router.now = func() time.Time { return now }
	member := Endpoint{ID: "node-a", Address: "127.0.0.1:443", Weight: 1, Status: EndpointReady, HealthLeaseExpiresAt: now.Add(time.Minute)}
	if err := router.ReplaceVersionedMembership(1, []Endpoint{member}); err != nil {
		t.Fatal(err)
	}
	if err := router.SetProbeReachabilityAtRevision(member.ID, member.Address, 1, false); err != nil {
		t.Fatal(err)
	}
	if err := router.SetLocalReachabilityAtRevision(member.ID, member.Address, 1, false); err != nil {
		t.Fatal(err)
	}
	now = now.Add(3 * localFailureCooldown)
	member.HealthLeaseExpiresAt = now.Add(time.Minute)
	if err := router.ReplaceVersionedMembership(2, []Endpoint{member}); err != nil {
		t.Fatal(err)
	}
	if blocked, err := router.LocallyBlocked(member.ID); err != nil || !blocked {
		t.Fatalf("probe veto expired with dial cooldown: blocked=%v err=%v", blocked, err)
	}
	if _, err := router.Select(); !errors.Is(err, ErrNoHealthyEndpoint) {
		t.Fatalf("failed member became selectable: %v", err)
	}
	if err := router.SetProbeReachabilityAtRevision(member.ID, member.Address, 1, true); !errors.Is(err, ErrStaleReachabilityResult) {
		t.Fatalf("old probe cleared current veto: %v", err)
	}
	if err := router.SetProbeReachabilityAtRevision(member.ID, member.Address, 2, true); err != nil {
		t.Fatal(err)
	}
	selected, err := router.Select()
	if err != nil || selected.ID != member.ID {
		t.Fatalf("recovered member not selectable: selected=%v err=%v", selected, err)
	}
	if err := router.Release(selected.ID); err != nil {
		t.Fatal(err)
	}
}

func TestProbeRecoveryDoesNotClearNewerDialFailure(t *testing.T) {
	router, err := New(SelectionPolicyWeightedRoundRobin)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	router.now = func() time.Time { return now }
	member := Endpoint{ID: "node-a", Address: "127.0.0.1:443", Weight: 1, Status: EndpointReady, HealthLeaseExpiresAt: now.Add(time.Minute)}
	if err := router.ReplaceVersionedMembership(1, []Endpoint{member}); err != nil {
		t.Fatal(err)
	}
	if err := router.SetProbeReachabilityAtRevision(member.ID, member.Address, 1, false); err != nil {
		t.Fatal(err)
	}
	if err := router.SetLocalReachabilityAtRevision(member.ID, member.Address, 1, false); err != nil {
		t.Fatal(err)
	}
	if err := router.SetProbeReachabilityAtRevision(member.ID, member.Address, 1, true); err != nil {
		t.Fatal(err)
	}
	if _, err := router.Select(); !errors.Is(err, ErrNoHealthyEndpoint) {
		t.Fatalf("probe recovery cleared independent dial cooldown: %v", err)
	}
	now = now.Add(localFailureCooldown + time.Nanosecond)
	selected, err := router.Select()
	if err != nil {
		t.Fatal(err)
	}
	if err := router.Release(selected.ID); err != nil {
		t.Fatal(err)
	}
}

func TestProbeVetoIsRemovedWhenAddressChangesOrMemberIsRemoved(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(map[bool]string{false: "new-address", true: "remove-and-readd"}[remove], func(t *testing.T) {
			router, err := New(SelectionPolicyWeightedRoundRobin)
			if err != nil {
				t.Fatal(err)
			}
			member := Endpoint{ID: "node-a", Address: "127.0.0.1:443", Weight: 1, Status: EndpointReady, HealthLeaseExpiresAt: time.Now().Add(time.Minute)}
			if err := router.ReplaceVersionedMembership(1, []Endpoint{member}); err != nil {
				t.Fatal(err)
			}
			if err := router.SetProbeReachabilityAtRevision(member.ID, member.Address, 1, false); err != nil {
				t.Fatal(err)
			}
			if remove {
				if err := router.ReplaceVersionedMembership(2, []Endpoint{}); err != nil {
					t.Fatal(err)
				}
			} else {
				member.Address = "127.0.0.1:444"
			}
			if err := router.ReplaceVersionedMembership(3, []Endpoint{member}); err != nil {
				t.Fatal(err)
			}
			if blocked, err := router.LocallyBlocked(member.ID); err != nil || blocked {
				t.Fatalf("old identity retained probe veto: blocked=%v err=%v", blocked, err)
			}
		})
	}
}
