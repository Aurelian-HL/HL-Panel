package memoryrepo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
)

func TestDeleteEndpointPoolChecksDependenciesAndReplaysWithoutSecondAudit(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	hash, err := auth.HashPassword("unit-test-administrator-password")
	if err != nil {
		t.Fatal(err)
	}
	store := New(auth.Administrator{ID: "admin-1", Username: "admin", PasswordHash: hash, CreatedAt: now})
	store.endpointPools["pool-1"] = endpoints.EndpointPool{ID: "pool-1", OwnerID: "admin-1", RuleID: "rule-1"}
	store.endpointMembers["pool-1"] = map[string]endpoints.EndpointPoolMember{
		"node-1": {PoolID: "pool-1", NodeID: "node-1", ActiveConnections: 1},
	}
	store.endpointCreateKeys["admin-1\x00create-1"] = "pool-1"
	store.endpointCreateHashes["admin-1\x00create-1"] = "create-hash"
	input := endpoints.DeletePoolInput{
		PoolID: "pool-1", DeletedBy: "admin-1", DeletedAt: now,
		IdempotencyKey: "delete-1", RequestSHA256: "delete-hash",
	}
	event := projectionAudit("endpoint_pool.delete", now)

	foreign := input
	foreign.DeletedBy = "admin-2"
	if _, err := store.DeleteEndpointPool(ctx, foreign, event); !errors.Is(err, faults.ErrNotFound) {
		t.Fatalf("foreign administrator can delete pool: %v", err)
	}
	store.vlessBindings["binding-1"] = vlessidentity.CredentialRecord{Binding: vlessidentity.Binding{
		ID: "binding-1", EndpointPoolID: "pool-1", State: vlessidentity.StateActive,
	}}
	if _, err := store.DeleteEndpointPool(ctx, input, event); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("active customer identity reference did not block deletion: %v", err)
	}
	revokedBinding := store.vlessBindings["binding-1"]
	revokedBinding.Binding.State = vlessidentity.StateRevoked
	store.vlessBindings["binding-1"] = revokedBinding
	if _, err := store.DeleteEndpointPool(ctx, input, event); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("active connection did not block deletion after identity was revoked: %v", err)
	}
	delete(store.vlessBindings, "binding-1")
	store.vlessRuntimeMaterials["material-1"] = vlessruntime.Material{
		ID: "material-1", EndpointPoolID: "pool-1", State: vlessruntime.StateActive,
	}
	if _, err := store.DeleteEndpointPool(ctx, input, event); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("active runtime material did not block deletion: %v", err)
	}
	store.vlessRuntimeMaterials["material-1"] = vlessruntime.Material{
		ID: "material-1", EndpointPoolID: "pool-1", State: vlessruntime.StateRevoked,
	}
	if _, err := store.DeleteEndpointPool(ctx, input, event); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("active connection did not block deletion: %v", err)
	}
	delete(store.vlessRuntimeMaterials, "material-1")
	member := store.endpointMembers["pool-1"]["node-1"]
	member.ActiveConnections = 0
	store.endpointMembers["pool-1"]["node-1"] = member
	before := len(store.AuditEvents())
	if replayed, err := store.DeleteEndpointPool(ctx, input, event); err != nil || replayed {
		t.Fatalf("delete failed: replayed=%v err=%v", replayed, err)
	}
	if _, err := store.EndpointPool(ctx, input.PoolID); !errors.Is(err, faults.ErrNotFound) || store.endpointMembers[input.PoolID] != nil {
		t.Fatalf("deleted pool or candidate projection remains: %v", err)
	}
	if len(store.AuditEvents()) != before+1 {
		t.Fatal("successful delete did not append exactly one audit event")
	}
	if replayed, err := store.DeleteEndpointPool(ctx, input, event); err != nil || !replayed || len(store.AuditEvents()) != before+1 {
		t.Fatalf("idempotent retry failed: replayed=%v err=%v", replayed, err)
	}
	newKey := input
	newKey.IdempotencyKey = "delete-2"
	if _, err := store.DeleteEndpointPool(ctx, newKey, event); !errors.Is(err, faults.ErrNotFound) {
		t.Fatalf("new delete key on missing pool was not rejected: %v", err)
	}
	if _, _, err := store.CreateEndpointPool(ctx, endpoints.CreatePoolInput{
		CreatedBy: "admin-1", IdempotencyKey: "create-1", RequestSHA256: "create-hash",
	}, event); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("create retry returned deleted pool as zero value: %v", err)
	}
	if raw, err := store.EncodeSnapshot(); err != nil {
		t.Fatal(err)
	} else if restored, err := DecodeSnapshot(raw); err != nil {
		t.Fatal(err)
	} else if replayed, err := restored.DeleteEndpointPool(ctx, input, event); err != nil || !replayed {
		t.Fatalf("delete replay was not restored: replayed=%v err=%v", replayed, err)
	}
}

func TestDeleteEndpointPoolIgnoresRevokedIdentityHistory(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	store := New(auth.Administrator{ID: "admin-1", Username: "admin"})
	store.endpointPools["pool-revoked"] = endpoints.EndpointPool{ID: "pool-revoked", OwnerID: "admin-1", RuleID: "deleted-rule"}
	store.endpointMembers["pool-revoked"] = map[string]endpoints.EndpointPoolMember{}
	store.vlessBindings["binding-revoked"] = vlessidentity.CredentialRecord{Binding: vlessidentity.Binding{
		ID: "binding-revoked", EndpointPoolID: "pool-revoked", State: vlessidentity.StateRevoked,
	}}
	input := endpoints.DeletePoolInput{PoolID: "pool-revoked", DeletedBy: "admin-1", DeletedAt: now, IdempotencyKey: "delete-revoked", RequestSHA256: "delete-revoked-hash"}
	if replayed, err := store.DeleteEndpointPool(ctx, input, projectionAudit("delete-revoked", now)); err != nil || replayed {
		t.Fatalf("revoked identity history blocked deletion: replayed=%v err=%v", replayed, err)
	}
	if _, err := store.EndpointPool(ctx, input.PoolID); !errors.Is(err, faults.ErrNotFound) {
		t.Fatalf("revoked identity pool still exists: %v", err)
	}
}
