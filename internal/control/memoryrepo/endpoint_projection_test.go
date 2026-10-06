package memoryrepo

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

func TestDeviceGroupMembershipIsEndpointCandidateSourceOfTruth(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	store := New(auth.Administrator{ID: "admin-1", Username: "admin", CreatedAt: now})
	group := groups.DeviceGroup{
		ID: "group-1", Name: "edge", Kind: groups.KindEdge,
		SelectionPolicy: endpoints.SelectionWeightedLeastConnections,
		CreatedAt:       now, UpdatedAt: now,
	}
	if err := store.CreateDeviceGroup(ctx, group, projectionAudit("group-create", now)); err != nil {
		t.Fatal(err)
	}

	// Existing node heartbeats prove that control-plane liveness must not be
	// copied into protocol health when candidates are projected.
	for index := 1; index <= 10; index++ {
		nodeID := fmt.Sprintf("node-%02d", index)
		store.nodes[nodeID] = nodes.Node{ID: nodeID, Name: nodeID, LastHeartbeatAt: timePointer(now), CreatedAt: now, UpdatedAt: now}
		member := groups.Member{GroupID: group.ID, NodeID: nodeID, Weight: index * 10, Priority: index % 2, CreatedAt: now, UpdatedAt: now}
		if _, _, err := store.UpsertGroupMember(ctx, member, projectionAudit("group-member", now)); err != nil {
			t.Fatalf("add initial group member %s: %v", nodeID, err)
		}
	}

	first := createProjectedPool(t, store, "pool-1", "edge-a.example.test", group.ID, "pool-key-1", now)
	second := createProjectedPool(t, store, "pool-2", "edge-b.example.test", group.ID, "pool-key-2", now)
	for _, pool := range []endpoints.EndpointPool{first, second} {
		if pool.MemberCount != 10 || pool.HealthyCandidateCount != 0 {
			t.Fatalf("pool did not project ten unverified group members: %+v", pool)
		}
		members, err := store.EndpointPoolMembers(ctx, pool.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(members) != 10 {
			t.Fatalf("pool %s has %d candidates, want 10", pool.ID, len(members))
		}
		for _, member := range members {
			if member.LastHealthAt != nil {
				t.Fatalf("agent heartbeat incorrectly marked %s protocol-healthy", member.NodeID)
			}
		}
	}

	later := now.Add(time.Minute)
	store.nodes["node-11"] = nodes.Node{ID: "node-11", Name: "node-11", LastHeartbeatAt: timePointer(later), CreatedAt: later, UpdatedAt: later}
	if _, _, err := store.UpsertGroupMember(ctx, groups.Member{
		GroupID: group.ID, NodeID: "node-11", Weight: 110, Priority: 1, CreatedAt: later, UpdatedAt: later,
	}, projectionAudit("group-member-later", later)); err != nil {
		t.Fatal(err)
	}
	for _, poolID := range []string{first.ID, second.ID} {
		pool, err := store.EndpointPool(ctx, poolID)
		if err != nil {
			t.Fatal(err)
		}
		if pool.MemberCount != 11 {
			t.Fatalf("pool %s did not receive later group member: %+v", poolID, pool)
		}
		members, err := store.EndpointPoolMembers(ctx, poolID)
		if err != nil {
			t.Fatal(err)
		}
		if len(members) != 11 || members[10].NodeID != "node-11" || members[10].LastHealthAt != nil {
			t.Fatalf("unexpected later candidate projection for %s: %+v", poolID, members)
		}
	}

	verifiedAt := later.Add(10 * time.Second)
	store.mu.Lock()
	draining := store.endpointMembers[first.ID]["node-01"]
	draining.State = endpoints.CandidateDraining
	draining.LastHealthAt = timePointer(verifiedAt)
	draining.LastHealthReason = "operator drain"
	draining.ActiveConnections = 7
	store.endpointMembers[first.ID]["node-01"] = draining
	store.mu.Unlock()

	updatedAt := later.Add(20 * time.Second)
	if _, _, err := store.UpsertGroupMember(ctx, groups.Member{
		GroupID: group.ID, NodeID: "node-01", Weight: 250, Priority: 4, UpdatedAt: updatedAt,
	}, projectionAudit("group-member-weight", updatedAt)); err != nil {
		t.Fatal(err)
	}

	firstMembers, err := store.EndpointPoolMembers(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	firstUpdated := memberByNodeID(t, firstMembers, "node-01")
	if firstUpdated.Weight != 250 || firstUpdated.Priority != 4 || firstUpdated.State != endpoints.CandidateDraining || firstUpdated.ActiveConnections != 7 || firstUpdated.LastHealthAt == nil || !firstUpdated.LastHealthAt.Equal(verifiedAt) || firstUpdated.LastHealthReason != "operator drain" {
		t.Fatalf("group weight sync overwrote endpoint lifecycle state: %+v", firstUpdated)
	}
	secondMembers, err := store.EndpointPoolMembers(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondUpdated := memberByNodeID(t, secondMembers, "node-01")
	if secondUpdated.Weight != 250 || secondUpdated.Priority != 4 || secondUpdated.State != endpoints.CandidateEligible || secondUpdated.LastHealthAt != nil {
		t.Fatalf("group weight sync did not preserve independent endpoint state: %+v", secondUpdated)
	}

	_, _, err = store.AddEndpointPoolMember(ctx, endpoints.AddMemberInput{
		PoolID: first.ID, NodeID: "node-01", Weight: 999, Priority: 4,
		IdempotencyKey: "compat-mismatch", RequestSHA256: "mismatch", CreatedBy: "admin-1", CreatedAt: updatedAt,
	}, projectionAudit("endpoint-member-confirm", updatedAt))
	if !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("endpoint member API changed authoritative group weight: %v", err)
	}
	confirmation := endpoints.AddMemberInput{
		PoolID: first.ID, NodeID: "node-01", Weight: 250, Priority: 4,
		IdempotencyKey: "compat-confirm", RequestSHA256: "matching", CreatedBy: "admin-1", CreatedAt: updatedAt,
	}
	confirmed, replayed, err := store.AddEndpointPoolMember(ctx, confirmation, projectionAudit("endpoint-member-confirm", updatedAt))
	if err != nil {
		t.Fatal(err)
	}
	if replayed || confirmed.State != endpoints.CandidateDraining || confirmed.Weight != 250 {
		t.Fatalf("compatibility member confirmation changed projection: replayed=%v member=%+v", replayed, confirmed)
	}
	confirmed, replayed, err = store.AddEndpointPoolMember(ctx, confirmation, projectionAudit("endpoint-member-replay", updatedAt))
	if err != nil || !replayed || confirmed.Weight != 250 {
		t.Fatalf("unchanged compatibility confirmation did not replay: replayed=%v member=%+v err=%v", replayed, confirmed, err)
	}
	store.mu.Lock()
	delete(store.endpointMembers[first.ID], "node-01")
	store.mu.Unlock()
	if member, replayed, err := store.AddEndpointPoolMember(ctx, confirmation, projectionAudit("endpoint-member-missing-replay", updatedAt)); !errors.Is(err, faults.ErrConflict) || replayed || member.NodeID != "" {
		t.Fatalf("missing candidate replay returned a zero-value success: replayed=%v member=%+v err=%v", replayed, member, err)
	}
	// An idempotent group upsert repairs a missing projection from the sole
	// source of truth without changing its weight or priority.
	if _, _, err := store.UpsertGroupMember(ctx, groups.Member{
		GroupID: group.ID, NodeID: "node-01", Weight: 250, Priority: 4, UpdatedAt: updatedAt,
	}, projectionAudit("group-member-repair", updatedAt)); err != nil {
		t.Fatal(err)
	}

	changedAt := updatedAt.Add(time.Minute)
	if _, _, err := store.UpsertGroupMember(ctx, groups.Member{
		GroupID: group.ID, NodeID: "node-01", Weight: 300, Priority: 4, UpdatedAt: changedAt,
	}, projectionAudit("group-member-second-weight", changedAt)); err != nil {
		t.Fatal(err)
	}
	if member, replayed, err := store.AddEndpointPoolMember(ctx, confirmation, projectionAudit("endpoint-member-stale-replay", changedAt)); !errors.Is(err, faults.ErrConflict) || replayed || member.NodeID != "" {
		t.Fatalf("stale confirmation replay bypassed current group weight: replayed=%v member=%+v err=%v", replayed, member, err)
	}

	retiredAt := changedAt.Add(time.Minute)
	if _, _, err := store.UpsertGroupMember(ctx, groups.Member{
		GroupID: group.ID, NodeID: "node-01", Weight: 300, Priority: 4, RetiredAt: &retiredAt, UpdatedAt: retiredAt,
	}, projectionAudit("group-member-retire", retiredAt)); err != nil {
		t.Fatal(err)
	}
	if member, replayed, err := store.AddEndpointPoolMember(ctx, confirmation, projectionAudit("endpoint-member-retired-replay", retiredAt)); !errors.Is(err, faults.ErrConflict) || replayed || member.NodeID != "" {
		t.Fatalf("retired confirmation replay returned a member: replayed=%v member=%+v err=%v", replayed, member, err)
	}
}

func createProjectedPool(t *testing.T, store *Store, id, hostname, groupID, key string, now time.Time) endpoints.EndpointPool {
	t.Helper()
	pool, replayed, err := store.CreateEndpointPool(context.Background(), endpoints.CreatePoolInput{
		ID: id, Name: id, GroupID: groupID, Mode: endpoints.ModeSingleServiceEndpoint,
		Protocol: "vless", Hostname: hostname, Port: 443,
		SelectionPolicy: endpoints.SelectionWeightedLeastConnections,
		IdempotencyKey:  key, RequestSHA256: key, CreatedBy: "admin-1", CreatedAt: now,
	}, projectionAudit("endpoint-create", now))
	if err != nil {
		t.Fatal(err)
	}
	if replayed {
		t.Fatalf("new endpoint pool %s was marked replayed", id)
	}
	return pool
}

func TestGatewayRevisionAndAddressChangeInvalidateHealth(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := New(auth.Administrator{ID: "admin-1", Username: "admin", CreatedAt: now})
	group := groups.DeviceGroup{ID: "group-1", Name: "edge", Kind: groups.KindEdge, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateDeviceGroup(ctx, group, projectionAudit("create-group", now)); err != nil {
		t.Fatal(err)
	}
	store.nodes["node-1"] = nodes.Node{ID: "node-1", Name: "node-1", CreatedAt: now, UpdatedAt: now}
	member := groups.Member{GroupID: group.ID, NodeID: "node-1", DialHost: "first.example.test", Weight: 2, CreatedAt: now, UpdatedAt: now}
	if _, _, err := store.UpsertGroupMember(ctx, member, projectionAudit("add-member", now)); err != nil {
		t.Fatal(err)
	}
	pool := createProjectedPool(t, store, "pool-1", "gateway.example.test", group.ID, "create-pool", now)
	first, err := store.GatewayMembershipState(ctx, pool.ID)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := store.GatewayMembershipState(ctx, pool.ID)
	if err != nil || repeated.Revision != first.Revision || repeated.Members[0].DialHost != first.Members[0].DialHost {
		t.Fatalf("unchanged gateway state changed: first=%+v repeated=%+v err=%v", first, repeated, err)
	}
	store.mu.Lock()
	candidate := store.endpointMembers[pool.ID]["node-1"]
	candidate.LastHealthAt = timePointer(now)
	store.endpointMembers[pool.ID]["node-1"] = candidate
	store.mu.Unlock()
	member.DialHost = "second.example.test"
	member.UpdatedAt = now.Add(time.Second)
	if _, _, err := store.UpsertGroupMember(ctx, member, projectionAudit("change-address", member.UpdatedAt)); err != nil {
		t.Fatal(err)
	}
	changed, err := store.GatewayMembershipState(ctx, pool.ID)
	if err != nil || changed.Revision <= first.Revision || len(changed.Members) != 1 ||
		changed.Members[0].DialHost != "second.example.test" || changed.Members[0].LastHealthAt != nil ||
		changed.Members[0].LastHealthReason != "address_changed" {
		t.Fatalf("address change retained stale health or revision: first=%+v changed=%+v err=%v", first, changed, err)
	}
}

func memberByNodeID(t *testing.T, members []endpoints.EndpointPoolMember, nodeID string) endpoints.EndpointPoolMember {
	t.Helper()
	for _, member := range members {
		if member.NodeID == nodeID {
			return member
		}
	}
	t.Fatalf("candidate %s not found", nodeID)
	return endpoints.EndpointPoolMember{}
}

func projectionAudit(id string, now time.Time) audit.Event {
	return audit.Event{ID: id, ActorType: "administrator", ActorID: "admin-1", Action: id, ResourceType: "test", ResourceID: id, Outcome: "succeeded", CreatedAt: now}
}
