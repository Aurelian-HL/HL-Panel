package memoryrepo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/groups"
)

func TestDeleteDeviceGroupRefusesReferencesAndReplays(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)
	hash, err := auth.HashPassword("delete-test-password")
	if err != nil {
		t.Fatal(err)
	}
	store := New(auth.Administrator{ID: "admin-1", Username: "admin", PasswordHash: hash, CreatedAt: now})
	group := groups.DeviceGroup{ID: "group-delete", Name: "delete-me", Kind: groups.KindEntry, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateDeviceGroup(ctx, group, projectionAudit("create", now)); err != nil {
		t.Fatal(err)
	}
	input := groups.DeleteInput{GroupID: group.ID, DeletedBy: "admin-1", IdempotencyKey: "delete-1", RequestSHA256: "hash-1", DeletedAt: now}
	event := projectionAudit("delete", now)

	checks := []struct {
		name  string
		setup func()
		clear func()
	}{
		{"member", func() {
			store.membersByGroup[group.ID]["node-1"] = groups.Member{GroupID: group.ID, NodeID: "node-1", RetiredAt: timePointer(now)}
		}, func() { clearMap(store.membersByGroup[group.ID]) }},
		{"network", func() {
			store.groupNetworks[group.ID] = groupconfig.GroupNetwork{GroupID: group.ID}
		}, func() { delete(store.groupNetworks, group.ID) }},
		{"network reverse reference", func() {
			store.groupNetworks["other-network"] = groupconfig.GroupNetwork{GroupID: "other-network", AllowedExitGroupIDs: []string{group.ID}, FallbackExitGroupID: group.ID}
		}, func() { delete(store.groupNetworks, "other-network") }},
		{"user group", func() {
			store.userGroups["user-group"] = customers.UserGroup{ID: "user-group", AllowedEntryGroupIDs: []string{group.ID}}
		}, func() { delete(store.userGroups, "user-group") }},
		{"forwarding rule", func() {
			store.forwardRules["rule"] = forwarding.Rule{ID: "rule", EntryGroupID: group.ID}
		}, func() { delete(store.forwardRules, "rule") }},
		{"endpoint pool", func() {
			store.endpointPools["pool"] = endpoints.EndpointPool{ID: "pool", GroupID: group.ID}
		}, func() { delete(store.endpointPools, "pool") }},
		{"published revision", func() {
			store.revisionsByGroup[group.ID] = []generations.GroupRevision{{ID: "revision", GroupID: group.ID, Revision: 1}}
		}, func() { delete(store.revisionsByGroup, group.ID) }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			check.setup()
			defer check.clear()
			if replayed, err := store.DeleteDeviceGroup(ctx, input, event); !errors.Is(err, faults.ErrConflict) || replayed {
				t.Fatalf("delete with %s reference: replayed=%v err=%v", check.name, replayed, err)
			}
		})
	}

	beforeAudit := len(store.AuditEvents())
	if replayed, err := store.DeleteDeviceGroup(ctx, input, event); err != nil || replayed {
		t.Fatalf("unreferenced delete failed: replayed=%v err=%v", replayed, err)
	}
	if _, err := store.ListGroupMembers(ctx, group.ID); !errors.Is(err, faults.ErrNotFound) {
		t.Fatalf("deleted group member projection remains: %v", err)
	}
	if len(store.AuditEvents()) != beforeAudit+1 {
		t.Fatalf("successful delete appended %d audit events, want 1", len(store.AuditEvents())-beforeAudit)
	}
	if replayed, err := store.DeleteDeviceGroup(ctx, input, event); err != nil || !replayed {
		t.Fatalf("delete retry failed: replayed=%v err=%v", replayed, err)
	}
	if len(store.AuditEvents()) != beforeAudit+1 {
		t.Fatal("idempotent retry appended a duplicate audit event")
	}
	conflictHash := input
	conflictHash.RequestSHA256 = "different-hash"
	if _, err := store.DeleteDeviceGroup(ctx, conflictHash, event); !errors.Is(err, faults.ErrIdempotencyConflict) {
		t.Fatalf("same idempotency key with different hash returned %v", err)
	}
	if raw, err := store.EncodeSnapshot(); err != nil {
		t.Fatal(err)
	} else if restored, err := DecodeSnapshot(raw); err != nil {
		t.Fatal(err)
	} else if replayed, err := restored.DeleteDeviceGroup(ctx, input, event); err != nil || !replayed {
		t.Fatalf("delete replay was not restored from snapshot: replayed=%v err=%v", replayed, err)
	}
}

func clearMap[T any](items map[string]T) {
	for key := range items {
		delete(items, key)
	}
}
