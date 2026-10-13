package endpointrouter

import (
	"errors"
	"testing"
	"time"
)

func TestZeroWeightRetainsExistingConnectionButStopsNewSelection(t *testing.T) {
	router, err := New(SelectionPolicyWeightedRoundRobin)
	if err != nil {
		t.Fatal(err)
	}
	member := Endpoint{ID: "node-a", Address: "127.0.0.1:443", Weight: 10, Status: EndpointReady, HealthLeaseExpiresAt: time.Now().Add(time.Minute)}
	if err := router.ReplaceMembership([]Endpoint{member}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.Select(); err != nil {
		t.Fatal(err)
	}
	member.Weight = 0
	if err := router.ReplaceMembership([]Endpoint{member}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.Select(); !errors.Is(err, ErrNoHealthyEndpoint) {
		t.Fatalf("new connection error = %v", err)
	}
	if active, err := router.ActiveConnections(member.ID); err != nil || active != 1 {
		t.Fatalf("existing connection count = %d, error = %v", active, err)
	}
	if err := router.Release(member.ID); err != nil {
		t.Fatal(err)
	}
}

func TestLocalFailureVetoExpiresForRecoveryWhenProbingIsDisabled(t *testing.T) {
	router, err := New(SelectionPolicyWeightedRoundRobin)
	if err != nil {
		t.Fatal(err)
	}
	current := time.Unix(100, 0)
	router.now = func() time.Time { return current }
	member := Endpoint{ID: "node-a", Address: "127.0.0.1:443", Weight: 1, Status: EndpointReady, HealthLeaseExpiresAt: current.Add(time.Minute)}
	if err := router.ReplaceMembership([]Endpoint{member}); err != nil {
		t.Fatal(err)
	}
	if err := router.SetLocalReachability(member.ID, member.Address, false); err != nil {
		t.Fatal(err)
	}
	if blocked, err := router.LocallyBlocked(member.ID); err != nil || !blocked {
		t.Fatalf("local veto = %v, error = %v", blocked, err)
	}
	if _, err := router.Select(); !errors.Is(err, ErrNoHealthyEndpoint) {
		t.Fatalf("selection during cooldown = %v", err)
	}
	current = current.Add(localFailureCooldown + time.Nanosecond)
	selected, err := router.Select()
	if err != nil || selected.ID != member.ID {
		t.Fatalf("selection after cooldown = %#v, error = %v", selected, err)
	}
	if err := router.Release(member.ID); err != nil {
		t.Fatal(err)
	}
}

func TestStaleVersionedReachabilityCannotChangeNewMembership(t *testing.T) {
	router, err := New(SelectionPolicyWeightedRoundRobin)
	if err != nil {
		t.Fatal(err)
	}
	member := Endpoint{ID: "node-a", Address: "127.0.0.1:443", Weight: 1, Status: EndpointReady, HealthLeaseExpiresAt: time.Now().Add(time.Minute)}
	if err := router.ReplaceVersionedMembership(10, []Endpoint{member}); err != nil {
		t.Fatal(err)
	}
	selected, revision, err := router.SelectKeyWithRevision("client-a")
	if err != nil {
		t.Fatal(err)
	}
	if selected.ID != member.ID || revision != 10 {
		t.Fatalf("selected endpoint = %#v, revision = %d; want node-a at revision 10", selected, revision)
	}
	if err := router.Release(selected.ID); err != nil {
		t.Fatal(err)
	}
	member.Status = EndpointOffline
	if err := router.ReplaceVersionedMembership(11, []Endpoint{member}); err != nil {
		t.Fatal(err)
	}
	if err := router.SetLocalReachabilityAtRevision(selected.ID, selected.Address, revision, false); !errors.Is(err, ErrStaleReachabilityResult) {
		t.Fatalf("stale reachability error = %v, want ErrStaleReachabilityResult", err)
	}
	if blocked, err := router.LocallyBlocked(member.ID); err != nil || blocked {
		t.Fatalf("stale result changed local veto = %v, error = %v", blocked, err)
	}
}
