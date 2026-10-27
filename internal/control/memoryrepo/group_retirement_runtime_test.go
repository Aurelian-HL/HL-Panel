package memoryrepo

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
	"github.com/hongle/hl-panel/internal/securetoken"
)

func TestRetireNativeMemberSnapshotRemainsReadable(t *testing.T) {
	f := activeNativeMemberFixture(t)
	ctx := context.Background()
	member := nativeJoiningMember(f)
	node := cloneNode(f.store.nodes[f.nodeID])
	node.ID, node.CredentialHash = member.NodeID, securetoken.Hash("retirement-peer")
	f.store.nodes[node.ID] = node
	f.store.nodesByCredential[node.CredentialHash] = node.ID
	if _, _, err := f.store.UpsertGroupMember(ctx, member, projectionAudit("join", member.UpdatedAt)); err != nil {
		t.Fatal(err)
	}
	before, _, err := f.store.DesiredNodeConfig(ctx, member.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	ruleBefore := f.store.forwardRules[f.rule.ID]
	bindingBefore := f.store.vlessBindings[f.bindingID]
	peerBefore, err := f.store.RuntimeMaterialForNode(ctx, member.NodeID, f.bindingID)
	if err != nil {
		t.Fatal(err)
	}
	retiredAt := time.Now().UTC().Add(time.Minute)
	if _, err := f.store.RetireGroupMember(ctx, member.GroupID, f.nodeID, retiredAt, projectionAudit("retire-native", retiredAt)); err != nil {
		t.Fatal(err)
	}
	raw, err := f.store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatalf("retirement poisoned the persisted snapshot: %v", err)
	}
	for _, store := range []*Store{f.store, restored} {
		requireRetiredMaterialUnavailable(t, store, f)
	}
	after, _, err := f.store.DesiredNodeConfig(ctx, member.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Generation != after.Generation || !bytes.Equal(before.Config, after.Config) {
		t.Fatal("retiring another member changed the surviving node configuration")
	}
	peerAfter, err := f.store.RuntimeMaterialForNode(ctx, member.NodeID, f.bindingID)
	if err != nil || !reflect.DeepEqual(peerBefore, peerAfter) {
		t.Fatal("retirement changed the surviving node material", err)
	}
	if !reflect.DeepEqual(ruleBefore, f.store.forwardRules[f.rule.ID]) || !reflect.DeepEqual(bindingBefore, f.store.vlessBindings[f.bindingID]) {
		t.Fatal("retirement changed customer rule or subscription identity")
	}
}

func TestDeleteAlreadyRetiredNativeNodePreservesSurvivingForwarding(t *testing.T) {
	f := activeNativeMemberFixture(t)
	ctx := context.Background()
	member := nativeJoiningMember(f)
	peer := cloneNode(f.store.nodes[f.nodeID])
	peer.ID, peer.CredentialHash = member.NodeID, securetoken.Hash("retired-deletion-peer")
	f.store.nodes[peer.ID] = peer
	f.store.nodesByCredential[peer.CredentialHash] = peer.ID
	if _, _, err := f.store.UpsertGroupMember(ctx, member, projectionAudit("join-peer", member.UpdatedAt)); err != nil {
		t.Fatal(err)
	}
	before, _, err := f.store.DesiredNodeConfig(ctx, peer.ID)
	if err != nil {
		t.Fatal(err)
	}
	ruleBefore := f.store.forwardRules[f.rule.ID]
	bindingBefore := f.store.vlessBindings[f.bindingID]
	at := time.Now().UTC().Add(time.Minute)
	if _, err := f.store.RetireGroupMember(ctx, f.rule.EntryGroupID, f.nodeID, at, projectionAudit("retire-before-delete", at)); err != nil {
		t.Fatal(err)
	}
	input := nodes.DeleteInput{NodeID: f.nodeID, DeletedBy: "admin", IdempotencyKey: "delete-retired-node", RequestSHA256: "delete-retired-node-hash", DeletedAt: at.Add(time.Minute), OnlineFor: time.Minute}
	result, replayed, err := f.store.DeleteOffline(ctx, input, projectionAudit("delete-retired-node", input.DeletedAt))
	if err != nil || replayed {
		t.Fatal("retired node deletion failed", err)
	}
	raw, err := f.store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal("deleting an already retired node made all panel state unreadable", err)
	}
	requireRetiredMaterialUnavailable(t, restored, f)
	after, _, err := restored.DesiredNodeConfig(ctx, peer.ID)
	if err != nil || before.Generation != after.Generation || !bytes.Equal(before.Config, after.Config) {
		t.Fatal("deleting a retired peer changed the surviving node configuration", err)
	}
	if !reflect.DeepEqual(ruleBefore, restored.forwardRules[f.rule.ID]) || !reflect.DeepEqual(bindingBefore, restored.vlessBindings[f.bindingID]) {
		t.Fatal("node deletion changed the rule or subscription identity")
	}
	replay, replayed, err := restored.DeleteOffline(ctx, input, projectionAudit("delete-replay", input.DeletedAt))
	if err != nil || !replayed || !reflect.DeepEqual(result, replay) {
		t.Fatal("node deletion did not survive durable idempotent replay", err)
	}
}

func requireRetiredMaterialUnavailable(t *testing.T, store *Store, f vlessBundleFixture) {
	t.Helper()
	material, err := store.RuntimeMaterialForNode(context.Background(), f.nodeID, f.bindingID)
	if !errors.Is(err, faults.ErrNotFound) || !reflect.DeepEqual(material, vlessruntime.Material{}) {
		t.Fatal("inactive node received runtime material or an unexpected error")
	}
}

func TestInactiveNativeMemberLifecycleRemainsReadable(t *testing.T) {
	for _, operation := range []string{"delete-retired", "delete-offline", "move-group", "rejoin", "rejoin-revoked"} {
		t.Run(operation, func(t *testing.T) {
			f := activeNativeMemberFixture(t)
			ctx := context.Background()
			at := time.Now().UTC().Add(time.Minute)
			original := cloneVLESSRuntimeMaterial(f.store.vlessRuntimeMaterials[f.materialID])
			switch operation {
			case "delete-offline":
				_, _, err := f.store.DeleteOffline(ctx, nodes.DeleteInput{NodeID: f.nodeID, DeletedBy: "admin", IdempotencyKey: "delete-native", RequestSHA256: "delete-native-hash", DeletedAt: at, OnlineFor: time.Minute}, projectionAudit("delete-native", at))
				if err != nil {
					t.Fatal(err)
				}
			case "move-group":
				group := groups.DeviceGroup{ID: "other-entry", Name: "other-entry", Kind: groups.KindEntry, SelectionPolicy: endpoints.SelectionWeightedRoundRobin, CreatedAt: at, UpdatedAt: at}
				if err := f.store.CreateDeviceGroup(ctx, group, projectionAudit("other-group", at)); err != nil {
					t.Fatal(err)
				}
				service := enrollment.NewService(f.store, func() time.Time { return at }, time.Hour)
				token, err := service.IssueForGroup(ctx, "admin", "moved-native", group.ID, time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := service.Reenroll(ctx, f.store.nodes[f.nodeID], enrollment.EnrollInput{RawToken: token.Token, Hostname: "moved.example.test", DialHost: "192.0.2.11", Platform: "linux", Architecture: "amd64", AgentVersion: "test"}); err != nil {
					t.Fatal(err)
				}
			default:
				if _, err := f.store.RetireGroupMember(ctx, f.rule.EntryGroupID, f.nodeID, at, projectionAudit("retire-native", at)); err != nil {
					t.Fatal(err)
				}
				if operation == "delete-retired" {
					_, err := f.store.DeleteRetiredGroupMember(ctx, groups.DeleteMemberInput{GroupID: f.rule.EntryGroupID, NodeID: f.nodeID, DeletedBy: "admin", IdempotencyKey: "delete-retired", RequestSHA256: "delete-retired-hash", ExpectedUpdatedAt: at, DeletedAt: at.Add(time.Second)}, projectionAudit("delete-retired", at))
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			requireRetiredMaterialUnavailable(t, f.store, f)
			if operation == "rejoin-revoked" {
				service, err := vlessruntime.NewService(f.store, func() time.Time { return at.Add(time.Second) })
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := service.Revoke(ctx, "admin", f.materialID, original.Revision, "revoke-retired"); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := f.store.EncodeSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			restored, err := DecodeSnapshot(raw)
			if err != nil {
				t.Fatal("inactive native member poisoned snapshot", err)
			}
			requireRetiredMaterialUnavailable(t, restored, f)
			if operation != "rejoin" && operation != "rejoin-revoked" {
				return
			}
			member := restored.membersByGroup[f.rule.EntryGroupID][f.nodeID]
			member.RetiredAt, member.UpdatedAt = nil, at.Add(2*time.Second)
			if _, _, err := restored.UpsertGroupMember(ctx, member, projectionAudit("rejoin-native", member.UpdatedAt)); err != nil {
				t.Fatal(err)
			}
			if operation == "rejoin-revoked" {
				requireRetiredMaterialUnavailable(t, restored, f)
				return
			}
			material, err := restored.RuntimeMaterialForNode(ctx, f.nodeID, f.bindingID)
			if err != nil || !reflect.DeepEqual(original, material) {
				t.Fatal("rejoining changed the original identity or native configuration", err)
			}
			f.store = restored
			requireAutomaticRuleBundle(t, f, f.nodeID)
		})
	}
}

func TestRetiredMaterialSnapshotStillRejectsCorruption(t *testing.T) {
	for _, corruption := range []string{"private-key", "binding", "native-config"} {
		t.Run(corruption, func(t *testing.T) {
			f := activeNativeMemberFixture(t)
			at := time.Now().UTC().Add(time.Minute)
			if _, err := f.store.RetireGroupMember(context.Background(), f.rule.EntryGroupID, f.nodeID, at, projectionAudit("retire", at)); err != nil {
				t.Fatal(err)
			}
			material := f.store.vlessRuntimeMaterials[f.materialID]
			switch corruption {
			case "private-key":
				material.RealityPrivateKey = "invalid"
			case "binding":
				material.BindingID = "missing-binding"
			case "native-config":
				material.NativeConfig = []byte(`{}`)
			}
			f.store.vlessRuntimeMaterials[material.ID] = material
			raw, err := f.store.EncodeSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeSnapshot(raw); err == nil {
				t.Fatal("retired node bypassed persisted material integrity checks")
			}
		})
	}
}
