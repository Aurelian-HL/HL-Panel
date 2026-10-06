package endpointselector

import (
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/routing/endpointrouter"
)

func TestRouterSelectorReservesAndReleasesBackend(t *testing.T) {
	router, err := endpointrouter.New(endpointrouter.SelectionPolicyRendezvousHash)
	if err != nil {
		t.Fatal(err)
	}
	if err := router.ReplaceMembership([]endpointrouter.Endpoint{
		{ID: "node-a", Address: "127.0.0.1:10001", Weight: 1, Status: endpointrouter.EndpointReady, HealthLeaseExpiresAt: time.Now().Add(time.Minute)},
		{ID: "node-b", Address: "127.0.0.1:10002", Weight: 1, Status: endpointrouter.EndpointReady, HealthLeaseExpiresAt: time.Now().Add(time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	selector, err := NewRouterSelector(router)
	if err != nil {
		t.Fatal(err)
	}
	first, err := selector.SelectKey("client-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := selector.Release(first.ID); err != nil {
		t.Fatal(err)
	}
	second, err := selector.SelectKey("client-a")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("rendezvous selection changed from %q to %q for the same key", first.ID, second.ID)
	}
	if err := selector.Release(second.ID); err != nil {
		t.Fatal(err)
	}
	if active, err := router.ActiveConnections(first.ID); err != nil || active != 0 {
		t.Fatalf("active connections = %d, error = %v; want zero", active, err)
	}
}

func TestNewRouterSelectorRequiresRouter(t *testing.T) {
	if _, err := NewRouterSelector(nil); !errors.Is(err, ErrRouterRequired) {
		t.Fatalf("NewRouterSelector(nil) error = %v, want ErrRouterRequired", err)
	}
}
