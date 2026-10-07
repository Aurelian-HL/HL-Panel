package memoryrepo

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
)

func TestVLESSAndEndpointPoolsAreScopedToOwningAdministrator(t *testing.T) {
	fixture := newVLESSBundleFixture(t, true)
	ctx := context.Background()
	owner, other := "admin", "other-admin"
	poolService := endpoints.NewService(fixture.store, nil)
	for _, check := range []struct {
		admin string
		want  int
	}{
		{owner, 1}, {other, 0},
	} {
		pools, err := poolService.ListPoolsForAdministrator(ctx, check.admin)
		if err != nil || len(pools) != check.want {
			t.Fatalf("endpoint list for %s: count=%d err=%v", check.admin, len(pools), err)
		}
		bindings, err := fixture.store.ListForAdministrator(ctx, check.admin)
		if err != nil || len(bindings) != check.want {
			t.Fatalf("identity list for %s: count=%d err=%v", check.admin, len(bindings), err)
		}
		materials, err := fixture.store.ListPublicRuntimeMaterialsForAdministrator(ctx, check.admin)
		if err != nil || len(materials) != check.want {
			t.Fatalf("runtime list for %s: count=%d err=%v", check.admin, len(materials), err)
		}
	}
	when := time.Date(2026, 10, 3, 13, 0, 0, 0, time.UTC)
	event, err := audit.NewEvent(when, "administrator", other, "test", "test", "test", "succeeded", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.store.AddEndpointPoolMember(ctx, endpoints.AddMemberInput{
		PoolID: fixture.endpointID, NodeID: fixture.nodeID, Weight: 1, CreatedBy: other,
		IdempotencyKey: "cross-owner", RequestSHA256: "cross-owner", CreatedAt: when,
	}, event); !errors.Is(err, faults.ErrNotFound) {
		t.Fatalf("other admin mutated endpoint pool: %v", err)
	}
	if _, _, err := fixture.store.Rotate(ctx, vlessidentity.RotateInput{
		BindingID: fixture.bindingID, CredentialUUID: "4d506676-e698-44f0-b7ae-bc56ae48f394",
		ExpectedRevision: 1, IdempotencyKey: "cross-owner", RequestSHA256: strings.Repeat("a", 64),
		UpdatedBy: other, UpdatedAt: when,
	}, event); !errors.Is(err, faults.ErrNotFound) {
		t.Fatalf("other admin rotated identity: %v", err)
	}
	if _, _, err := fixture.store.RevokeRuntimeMaterial(ctx, vlessruntime.RevokeInput{
		ID: fixture.materialID, ExpectedRevision: 1, IdempotencyKey: "cross-owner",
		RequestSHA256: strings.Repeat("b", 64), UpdatedBy: other, UpdatedAt: when,
	}, event); !errors.Is(err, faults.ErrNotFound) {
		t.Fatalf("other admin revoked runtime material: %v", err)
	}
	if fixture.store.vlessBindings[fixture.bindingID].Binding.Revision != 1 || fixture.store.vlessRuntimeMaterials[fixture.materialID].State != vlessruntime.StateActive {
		t.Fatal("cross-owner mutation changed VLESS state")
	}
}

func TestVLESSIdentityRejectsUnboundEndpointPool(t *testing.T) {
	fixture := newVLESSBundleFixture(t, true)
	ctx := context.Background()
	sharedID := "pool-unbound"
	now := time.Date(2026, 10, 3, 14, 0, 0, 0, time.UTC)
	fixture.store.endpointPools[sharedID] = endpoints.EndpointPool{
		ID: sharedID, OwnerID: "admin", Name: "unbound-vless", GroupID: fixture.rule.EntryGroupID,
		Mode: endpoints.ModeSingleServiceEndpoint, Protocol: "vless", Hostname: "vless.example.test", Port: 443,
		SelectionPolicy: endpoints.SelectionWeightedRoundRobin, CreatedAt: now, UpdatedAt: now,
	}
	fixture.store.endpointMembers[sharedID] = map[string]endpoints.EndpointPoolMember{
		fixture.nodeID: {PoolID: sharedID, GroupID: fixture.rule.EntryGroupID, NodeID: fixture.nodeID,
			Weight: 1, Priority: 0, State: endpoints.CandidateEligible, UpdatedAt: now},
	}
	identity, err := vlessidentity.NewService(fixture.store, func() time.Time { return now }, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = identity.Provision(ctx, "admin", vlessidentity.ProvisionRequest{
		CustomerID: fixture.rule.CustomerID, ForwardingRuleID: fixture.rule.ID, EndpointPoolID: sharedID,
	}, "unbound-identity")
	if !errors.Is(err, faults.ErrNotFound) {
		t.Fatalf("unbound endpoint pool was accepted for a rule identity: %v", err)
	}
}
