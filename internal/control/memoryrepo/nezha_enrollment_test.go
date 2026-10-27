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
	for _, corruption := range []string{"missing-node", "other-live-binding", "unbound-legacy-token"} {
		t.Run(corruption, func(t *testing.T) {
			var state snapshot
			if err := json.Unmarshal(raw, &state); err != nil {
				t.Fatal(err)
			}
			token := state.Tokens["hash-1"]
			switch corruption {
			case "missing-node":
				token.UsedNodeID = "missing-node"
			case "other-live-binding":
				token.NezhaServerID = 8
			case "unbound-legacy-token":
				token.UsedNodeID, token.NezhaServerID = "", 8
			}
			state.Tokens["hash-1"] = token
			corrupt, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeSnapshot(corrupt); err == nil {
				t.Fatal("invalid consumed monitoring enrollment was accepted")
			}
		})
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

func TestConsumedNezhaEnrollmentSurvivesNodeDeletion(t *testing.T) {
	for _, lifecycle := range []string{"delete", "reuse-pending", "reuse-consumed", "reenroll", "legacy-delete"} {
		t.Run(lifecycle, func(t *testing.T) {
			ctx := context.Background()
			now := time.Now().UTC()
			store := snapshotFixture(t)
			event := projectionAudit("nezha-lifecycle", now)
			event.Metadata = make(map[string]any)
			group := groups.DeviceGroup{ID: "nezha-group", Name: "nezha-group", Kind: groups.KindEntry, CreatedAt: now, UpdatedAt: now}
			if err := store.CreateDeviceGroup(ctx, group, event); err != nil {
				t.Fatal(err)
			}
			token := enrollment.Token{ID: "nezha-token", Name: "nezha-node", GroupID: group.ID, NezhaServerID: 7, TokenHash: "nezha-token-hash", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
			if err := store.CreateEnrollmentToken(ctx, token, event); err != nil {
				t.Fatal(err)
			}
			node, err := store.ConsumeEnrollmentToken(ctx, enrollment.ConsumeInput{TokenHash: token.TokenHash, CredentialHash: "nezha-node-credential", Node: nodes.Node{ID: "nezha-node", Hostname: "edge.example.test", CreatedAt: now}}, now, event)
			if err != nil {
				t.Fatal(err)
			}
			if lifecycle == "legacy-delete" {
				legacy := store.tokensByHash[token.TokenHash]
				legacy.UsedNodeID = ""
				store.tokensByHash[token.TokenHash] = legacy
				store = requireNezhaSnapshotRoundTrip(t, store)
			}
			if _, _, err := store.DeleteOffline(ctx, nodes.DeleteInput{NodeID: node.ID, DeletedBy: "admin", IdempotencyKey: "nezha-delete", RequestSHA256: "nezha-delete-hash", DeletedAt: now.Add(time.Minute), OnlineFor: time.Minute}, event); err != nil {
				t.Fatal(err)
			}
			store = requireNezhaSnapshotRoundTrip(t, store)
			if lifecycle == "reuse-pending" || lifecycle == "reuse-consumed" {
				replacement := token
				replacement.ID, replacement.TokenHash = "replacement-token", "replacement-hash"
				if err := store.CreateEnrollmentToken(ctx, replacement, event); err != nil {
					t.Fatal("deleted node retained its monitoring reservation", err)
				}
				if lifecycle == "reuse-consumed" {
					if _, err := store.ConsumeEnrollmentToken(ctx, enrollment.ConsumeInput{TokenHash: replacement.TokenHash, CredentialHash: "replacement-credential", Node: nodes.Node{ID: "replacement-node", Hostname: "replacement.example.test", CreatedAt: now}}, now.Add(2*time.Minute), event); err != nil {
						t.Fatal(err)
					}
				}
			} else if lifecycle == "reenroll" {
				replacement := token
				replacement.ID, replacement.TokenHash, replacement.NezhaServerID = "reenroll-token", "reenroll-hash", 0
				if err := store.CreateEnrollmentToken(ctx, replacement, event); err != nil {
					t.Fatal(err)
				}
				if _, err := store.ConsumeEnrollmentToken(ctx, enrollment.ConsumeInput{TokenHash: replacement.TokenHash, CredentialHash: node.CredentialHash, Node: node, Reenroll: true}, now.Add(2*time.Minute), event); err != nil {
					t.Fatal(err)
				}
			}
			store = requireNezhaSnapshotRoundTrip(t, store)
			bindings, err := store.ListNezhaBindings(ctx)
			if err != nil || bindings[node.ID] != 0 {
				t.Fatal("historical enrollment restored a released monitoring binding", err)
			}
			if lifecycle == "reuse-consumed" && bindings["replacement-node"] != 7 {
				t.Fatal("monitoring ID was not bound to the replacement node")
			}
		})
	}
}

func requireNezhaSnapshotRoundTrip(t *testing.T, store *Store) *Store {
	t.Helper()
	raw, err := store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatalf("monitoring enrollment lifecycle poisoned the snapshot: %v", err)
	}
	return restored
}
