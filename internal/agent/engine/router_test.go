package engine

import (
	"errors"
	"testing"
	"time"
)

func TestEndpointRouterExcludesOfflineDrainingAndExpiredMembers(t *testing.T) {
	t.Parallel()
	router, err := NewEndpointRouter(SelectionPolicyWeightedRoundRobin)
	if err != nil {
		t.Fatalf("NewEndpointRouter() error = %v", err)
	}
	now := time.Now()
	if err := router.ReplaceMembership([]Endpoint{
		{ID: "ready", Address: "10.0.0.1:443", Weight: 1, Status: EndpointReady, HealthLeaseExpiresAt: now.Add(time.Minute)},
		{ID: "offline", Address: "10.0.0.2:443", Weight: 100, Status: EndpointOffline, HealthLeaseExpiresAt: now.Add(time.Minute)},
		{ID: "draining", Address: "10.0.0.3:443", Weight: 100, Status: EndpointDraining, HealthLeaseExpiresAt: now.Add(time.Minute)},
		{ID: "expired", Address: "10.0.0.4:443", Weight: 100, Status: EndpointReady, HealthLeaseExpiresAt: now},
	}); err != nil {
		t.Fatalf("ReplaceMembership() error = %v", err)
	}
	for i := 0; i < 5; i++ {
		selected, err := router.Select()
		if err != nil {
			t.Fatalf("Select() error = %v", err)
		}
		if selected.ID != "ready" {
			t.Fatalf("Select() = %#v, want ready member only", selected)
		}
		if err := router.Release(selected.ID); err != nil {
			t.Fatalf("Release() error = %v", err)
		}
	}
}

func TestWeightedRoundRobinHonorsRelativeWeights(t *testing.T) {
	t.Parallel()
	router, err := NewEndpointRouter(SelectionPolicyWeightedRoundRobin)
	if err != nil {
		t.Fatalf("NewEndpointRouter() error = %v", err)
	}
	now := time.Now()
	if err := router.ReplaceMembership([]Endpoint{
		{ID: "a", Address: "10.0.0.1:443", Weight: 1, Status: EndpointReady, HealthLeaseExpiresAt: now.Add(time.Minute)},
		{ID: "b", Address: "10.0.0.2:443", Weight: 3, Status: EndpointReady, HealthLeaseExpiresAt: now.Add(time.Minute)},
	}); err != nil {
		t.Fatalf("ReplaceMembership() error = %v", err)
	}
	counts := map[string]int{}
	for i := 0; i < 40; i++ {
		selected, err := router.Select()
		if err != nil {
			t.Fatalf("Select() error = %v", err)
		}
		counts[selected.ID]++
		if err := router.Release(selected.ID); err != nil {
			t.Fatalf("Release() error = %v", err)
		}
	}
	if counts["a"] != 10 || counts["b"] != 30 {
		t.Fatalf("weighted counts = %#v, want a=10 b=30", counts)
	}
}

func TestWeightedLeastConnectionsUsesWeightAndRelease(t *testing.T) {
	t.Parallel()
	router, err := NewEndpointRouter(SelectionPolicyWeightedLeastConnections)
	if err != nil {
		t.Fatalf("NewEndpointRouter() error = %v", err)
	}
	now := time.Now()
	if err := router.ReplaceMembership([]Endpoint{
		{ID: "small", Address: "10.0.0.1:443", Weight: 1, Status: EndpointReady, HealthLeaseExpiresAt: now.Add(time.Minute)},
		{ID: "large", Address: "10.0.0.2:443", Weight: 2, Status: EndpointReady, HealthLeaseExpiresAt: now.Add(time.Minute)},
	}); err != nil {
		t.Fatalf("ReplaceMembership() error = %v", err)
	}
	first, err := router.Select()
	if err != nil {
		t.Fatalf("first Select() error = %v", err)
	}
	second, err := router.Select()
	if err != nil {
		t.Fatalf("second Select() error = %v", err)
	}
	if first.ID != "large" || second.ID != "small" {
		t.Fatalf("selections = %q, %q; want large then small", first.ID, second.ID)
	}
	if err := router.Release(first.ID); err != nil {
		t.Fatalf("Release(first) error = %v", err)
	}
	if err := router.Release(second.ID); err != nil {
		t.Fatalf("Release(second) error = %v", err)
	}
	if _, err := router.Select(); err != nil {
		t.Fatalf("Select() after release error = %v", err)
	}
}

func TestEndpointRouterFailsClosedWithoutHealthyLease(t *testing.T) {
	t.Parallel()
	router, err := NewEndpointRouter(SelectionPolicyWeightedRoundRobin)
	if err != nil {
		t.Fatalf("NewEndpointRouter() error = %v", err)
	}
	if err := router.ReplaceMembership([]Endpoint{{ID: "missing", Address: "10.0.0.1:443", Weight: 1, Status: EndpointReady}}); err == nil {
		t.Fatalf("ReplaceMembership() unexpectedly accepted missing lease")
	}
	if err := router.ReplaceMembership([]Endpoint{{ID: "offline", Address: "10.0.0.1:443", Weight: 1, Status: EndpointOffline, HealthLeaseExpiresAt: time.Now().Add(time.Minute)}}); err != nil {
		t.Fatalf("ReplaceMembership(offline) error = %v", err)
	}
	if _, err := router.Select(); !errors.Is(err, ErrNoHealthyEndpoint) {
		t.Fatalf("Select() error = %v, want ErrNoHealthyEndpoint", err)
	}
}

func TestEndpointRouterReleasesConnectionAfterMemberRemoval(t *testing.T) {
	t.Parallel()
	router, err := NewEndpointRouter(SelectionPolicyWeightedRoundRobin)
	if err != nil {
		t.Fatalf("NewEndpointRouter() error = %v", err)
	}
	now := time.Now()
	endpoint := Endpoint{ID: "retiring", Address: "10.0.0.1:443", Weight: 1, Status: EndpointReady, HealthLeaseExpiresAt: now.Add(time.Minute)}
	if err := router.ReplaceMembership([]Endpoint{endpoint}); err != nil {
		t.Fatalf("ReplaceMembership() error = %v", err)
	}
	selected, err := router.Select()
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if err := router.ReplaceMembership(nil); err != nil {
		t.Fatalf("ReplaceMembership(empty) error = %v", err)
	}
	if err := router.Release(selected.ID); err != nil {
		t.Fatalf("Release(retired member) error = %v, want nil", err)
	}
	if _, err := router.ActiveConnections(selected.ID); !errors.Is(err, ErrUnknownEndpoint) {
		t.Fatalf("ActiveConnections(retired member) error = %v, want ErrUnknownEndpoint after release", err)
	}
}

func TestEndpointRouterAddressValidationAllowsDomainLettersAndRejectsControlCharacters(t *testing.T) {
	t.Parallel()
	router, err := NewEndpointRouter(SelectionPolicyWeightedRoundRobin)
	if err != nil {
		t.Fatalf("NewEndpointRouter() error = %v", err)
	}
	future := time.Now().Add(time.Minute)
	if err := router.ReplaceMembership([]Endpoint{{
		ID: "domain", Address: "entry.example.net:443", Weight: 1, Status: EndpointReady, HealthLeaseExpiresAt: future,
	}}); err != nil {
		t.Fatalf("ReplaceMembership(domain) error = %v", err)
	}
	for _, address := range []string{"host\r:443", "host\n:443", "host\t:443"} {
		if err := router.ReplaceMembership([]Endpoint{{
			ID: "control", Address: address, Weight: 1, Status: EndpointReady, HealthLeaseExpiresAt: future,
		}}); err == nil {
			t.Fatalf("ReplaceMembership() accepted control-character address %q", address)
		}
	}
}

func TestLocalReachabilityVetoSurvivesReloadButNotAddressReplacement(t *testing.T) {
	router, err := NewEndpointRouter(SelectionPolicyWeightedRoundRobin)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := Endpoint{ID: "node-a", Address: "127.0.0.1:10001", Weight: 1, Status: EndpointReady, HealthLeaseExpiresAt: time.Now().Add(time.Minute)}
	if err := router.ReplaceMembership([]Endpoint{endpoint}); err != nil {
		t.Fatal(err)
	}
	if err := router.SetLocalReachability(endpoint.ID, endpoint.Address, false); err != nil {
		t.Fatal(err)
	}
	endpoint.Weight = 9
	endpoint.HealthLeaseExpiresAt = time.Now().Add(2 * time.Minute)
	if err := router.ReplaceMembership([]Endpoint{endpoint}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.Select(); !errors.Is(err, ErrNoHealthyEndpoint) {
		t.Fatalf("Select() error = %v, want local veto to survive reload", err)
	}
	if err := router.SetLocalReachability(endpoint.ID, "127.0.0.1:19999", true); !errors.Is(err, ErrStaleReachabilityResult) {
		t.Fatalf("stale reachability error = %v", err)
	}
	endpoint.Address = "127.0.0.1:10002"
	if err := router.ReplaceMembership([]Endpoint{endpoint}); err != nil {
		t.Fatal(err)
	}
	selected, err := router.Select()
	if err != nil || selected.Address != endpoint.Address {
		t.Fatalf("address replacement remained vetoed: endpoint=%#v error=%v", selected, err)
	}
	if err := router.Release(selected.ID); err != nil {
		t.Fatal(err)
	}
}

func TestLocalReachabilityCannotPromoteOfflineOrExpiredEndpoint(t *testing.T) {
	router, err := NewEndpointRouter(SelectionPolicyWeightedRoundRobin)
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []Endpoint{
		{ID: "offline", Address: "127.0.0.1:10001", Weight: 1, Status: EndpointOffline, HealthLeaseExpiresAt: time.Now().Add(time.Minute)},
		{ID: "expired", Address: "127.0.0.1:10002", Weight: 1, Status: EndpointReady, HealthLeaseExpiresAt: time.Now().Add(-time.Second)},
	} {
		if err := router.ReplaceMembership([]Endpoint{endpoint}); err != nil {
			t.Fatal(err)
		}
		if err := router.SetLocalReachability(endpoint.ID, endpoint.Address, true); err != nil {
			t.Fatal(err)
		}
		if _, err := router.Select(); !errors.Is(err, ErrNoHealthyEndpoint) {
			t.Fatalf("Select(%s) error = %v, want authoritative exclusion", endpoint.ID, err)
		}
	}
}

func TestDelayedOldRevisionVerdictCannotClearSameAddressVeto(t *testing.T) {
	router, err := NewEndpointRouter(SelectionPolicyWeightedRoundRobin)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := Endpoint{
		ID: "node-a", Address: "127.0.0.1:10001", Weight: 1,
		Status: EndpointReady, HealthLeaseExpiresAt: time.Now().Add(time.Minute),
	}
	if err := router.ReplaceVersionedMembership(1, []Endpoint{endpoint}); err != nil {
		t.Fatal(err)
	}
	if err := router.SetLocalReachabilityAtRevision(endpoint.ID, endpoint.Address, 1, false); err != nil {
		t.Fatal(err)
	}
	endpoint.HealthLeaseExpiresAt = time.Now().Add(2 * time.Minute)
	if err := router.ReplaceVersionedMembership(2, []Endpoint{endpoint}); err != nil {
		t.Fatal(err)
	}
	if err := router.SetLocalReachabilityAtRevision(endpoint.ID, endpoint.Address, 1, true); !errors.Is(err, ErrStaleReachabilityResult) {
		t.Fatalf("old revision verdict error = %v, want stale result", err)
	}
	if blocked, err := router.LocallyBlocked(endpoint.ID); err != nil || !blocked {
		t.Fatalf("local veto after old verdict = %v, error = %v", blocked, err)
	}
	if _, err := router.Select(); !errors.Is(err, ErrNoHealthyEndpoint) {
		t.Fatalf("Select() error = %v, want veto to remain active", err)
	}
	if err := router.SetLocalReachabilityAtRevision(endpoint.ID, endpoint.Address, 2, true); err != nil {
		t.Fatal(err)
	}
	selected, err := router.Select()
	if err != nil || selected.ID != endpoint.ID {
		t.Fatalf("current revision recovery = %#v, error = %v", selected, err)
	}
	if err := router.Release(selected.ID); err != nil {
		t.Fatal(err)
	}
}

func TestVersionedMembershipRejectsChangedContentAtSameRevision(t *testing.T) {
	router, err := NewEndpointRouter(SelectionPolicyWeightedRoundRobin)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := Endpoint{
		ID: "node-a", Address: "127.0.0.1:10001", Weight: 1,
		Status: EndpointReady, HealthLeaseExpiresAt: time.Now().Add(time.Minute),
	}
	if err := router.ReplaceVersionedMembership(7, []Endpoint{endpoint}); err != nil {
		t.Fatal(err)
	}
	if err := router.ReplaceVersionedMembership(7, []Endpoint{endpoint}); err != nil {
		t.Fatalf("identical same-revision replay failed: %v", err)
	}
	changed := endpoint
	changed.Address = "127.0.0.1:10002"
	if err := router.ReplaceVersionedMembership(7, []Endpoint{changed}); !errors.Is(err, ErrMembershipConflict) {
		t.Fatalf("changed same-revision error = %v, want membership conflict", err)
	}
	selected, err := router.Select()
	if err != nil || selected.Address != endpoint.Address {
		t.Fatalf("active membership changed after conflict: endpoint=%#v error=%v", selected, err)
	}
	if err := router.Release(selected.ID); err != nil {
		t.Fatal(err)
	}
}
