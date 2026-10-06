package memoryrepo

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

func TestNezhaGroupEnrollmentPersistsOneToOneBinding(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	hash, err := auth.HashPassword("valid-test-password")
	if err != nil {
		t.Fatal(err)
	}
	store := New(auth.Administrator{ID: "admin", Username: "admin", PasswordHash: hash, CreatedAt: now})
	event, err := audit.NewEvent(now, "administrator", "admin", "test", "device_group", "group-1", "succeeded", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateDeviceGroup(ctx, groups.DeviceGroup{ID: "group-1", Name: "entry", Kind: groups.KindEntry, CreatedAt: now, UpdatedAt: now}, event); err != nil {
		t.Fatal(err)
	}
	token := enrollment.Token{ID: "token-1", Name: "node-1", GroupID: "group-1", NezhaServerID: 7, TokenHash: "hash-1", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := store.CreateEnrollmentToken(ctx, token, event); err != nil {
		t.Fatal(err)
	}
	duplicate := token
	duplicate.ID, duplicate.TokenHash = "token-2", "hash-2"
	if err := store.CreateEnrollmentToken(ctx, duplicate, event); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("duplicate reservation: %v", err)
	}
	pending, err := store.ListPendingEnrollmentTokens(ctx, "group-1", now)
	if err != nil || len(pending) != 1 || pending[0].NezhaServerID != 7 {
		t.Fatalf("pending token metadata: %+v, %v", pending, err)
	}
	node, err := store.ConsumeEnrollmentToken(ctx, enrollment.ConsumeInput{TokenHash: "hash-1", CredentialHash: "credential-1", Node: nodes.Node{ID: "node-1", Hostname: "edge.example.test", CreatedAt: now}}, now.Add(time.Minute), event)
	if err != nil || node.NezhaServerID != 7 {
		t.Fatalf("consume binding: %+v, %v", node, err)
	}
	if err := store.CreateEnrollmentToken(ctx, duplicate, event); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("bound ID reused: %v", err)
	}
	bindings, err := store.ListNezhaBindings(ctx)
	if err != nil || bindings["node-1"] != 7 {
		t.Fatalf("bindings: %+v, %v", bindings, err)
	}
	raw, err := store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	restoredBindings, err := restored.ListNezhaBindings(ctx)
	if err != nil || restoredBindings["node-1"] != 7 {
		t.Fatalf("restored bindings: %+v, %v", restoredBindings, err)
	}
}

func TestVersionThirteenSnapshotUpgradesWithoutNezhaBinding(t *testing.T) {
	store := snapshotFixture(t)
	raw, err := store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var legacy snapshot
	if err := json.Unmarshal(raw, &legacy); err != nil {
		t.Fatal(err)
	}
	legacy.Version = protocolProbeSnapshotVersion
	raw, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatalf("version 13 snapshot rejected: %v", err)
	}
	upgraded, err := restored.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var state snapshot
	if err := json.Unmarshal(upgraded, &state); err != nil || state.Version != SnapshotVersion {
		t.Fatalf("snapshot not upgraded to latest version: version=%d error=%v", state.Version, err)
	}
	legacy.Nodes["node-1"] = storedNode{Node: json.RawMessage(`{"id":"node-1","nezha_server_id":7}`), CredentialHash: "credential-hash"}
	corrupt, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSnapshot(corrupt); err == nil {
		t.Fatal("version 13 snapshot accepted a version 14 binding")
	}
}
