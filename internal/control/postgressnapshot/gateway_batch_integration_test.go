package postgressnapshot

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/groups"
)

type countedGatewayStore struct {
	*Store
	batches, singles int
}

func (s *countedGatewayStore) GatewayMembershipState(ctx context.Context, id string) (gatewaymembership.State, error) {
	s.singles++
	return s.Store.GatewayMembershipState(ctx, id)
}
func (s *countedGatewayStore) GatewayMembershipStates(ctx context.Context, ids []string) ([]gatewaymembership.State, error) {
	s.batches++
	return s.Store.GatewayMembershipStates(ctx, ids)
}

func TestPostgreSQLManyPoolsUseOneReadinessSnapshot(t *testing.T) {
	ctx := context.Background()
	admin := bootstrapAdministrator(t)
	store, err := Open(ctx, isolatedDatabaseURL(t), admin)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	group, err := groups.NewService(store, time.Now).Create(ctx, admin.ID, "batch test", groups.KindEntry, endpoints.SelectionWeightedRoundRobin, "group")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		_, _, err := store.CreateEndpointPool(ctx, endpoints.CreatePoolInput{
			ID: fmt.Sprintf("pool-%d", i), Name: fmt.Sprintf("pool-%d", i), GroupID: group.ID, Mode: endpoints.ModeSingleServiceEndpoint,
			Protocol: "vless", Hostname: "127.0.0.1", Port: 20000 + i, SelectionPolicy: endpoints.SelectionWeightedRoundRobin,
			CreatedBy: admin.ID, CreatedAt: time.Now(), IdempotencyKey: fmt.Sprintf("pool-%d", i), RequestSHA256: fmt.Sprintf("hash-%d", i),
		}, audit.Event{})
		if err != nil {
			t.Fatal(err)
		}
	}
	counted := &countedGatewayStore{Store: store}
	service := endpoints.NewServiceWithReadyCandidateSource(store, time.Now, gatewaymembership.NewService(counted))
	started := time.Now()
	items, err := service.ListPoolsForAdministrator(ctx, admin.ID)
	if err != nil || len(items) != 40 || counted.batches != 1 || counted.singles != 0 {
		t.Fatalf("many pools caused per-pool snapshot reads: count=%d batches=%d singles=%d err=%v", len(items), counted.batches, counted.singles, err)
	}
	for _, item := range items {
		if item.HealthyCandidateCount != 0 {
			t.Fatal("unbound pool invented readiness")
		}
	}
	t.Logf("40-pool PostgreSQL readiness list took %s using one batch snapshot", time.Since(started))
}
