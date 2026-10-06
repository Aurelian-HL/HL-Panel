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
