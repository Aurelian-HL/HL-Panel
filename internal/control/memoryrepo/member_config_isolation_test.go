package memoryrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// A broken configuration belonging only to a peer detects accidental group-wide
// compilation even when that peer's normal bundle would otherwise be unchanged.
func addBrokenMemberGroup(t *testing.T, store *Store, nodeID string, at time.Time) {
	t.Helper()
	group := groups.DeviceGroup{ID: "broken-" + nodeID, Name: "broken-" + nodeID, Kind: groups.KindEntry, CreatedAt: at, UpdatedAt: at}
	if err := store.CreateDeviceGroup(context.Background(), group, projectionAudit(group.ID, at)); err != nil {
		t.Fatal(err)
	}
	store.membersByGroup[group.ID][nodeID] = groups.Member{GroupID: group.ID, NodeID: nodeID, DialHost: "192.0.2.99", Weight: 1, CreatedAt: at, UpdatedAt: at}
	store.revisionsByGroup[group.ID] = []generations.GroupRevision{{GroupID: group.ID, Revision: 1, Engine: agentv1.EngineGOST, Config: json.RawMessage(`{broken`)}}
	if _, _, err := store.compileNodeConfigLocked(nodeID, at); err == nil {
		t.Fatal("fault injection did not reject the peer's configuration")
	}
}

func requireNoDesiredChange(t *testing.T, wake <-chan struct{}) {
	t.Helper()
	select {
	case <-wake:
		t.Fatal("unaffected or rolled-back node received a configuration notification")
	default:
	}
}

func TestMemberChangesDoNotCompileIndependentPeers(t *testing.T) {
	for _, operation := range []string{"add", "retire", "delete-offline", "reenroll", "heartbeat"} {
		t.Run(operation, func(t *testing.T) {
			f, service, input, targetID := nativeReenrollmentFixture(t)
			ctx := context.Background()
			at := time.Now().UTC().Add(time.Minute)
			if operation != "add" && operation != "reenroll" {
				if _, err := service.Reenroll(ctx, f.store.nodes[targetID], input); err != nil {
					t.Fatal(err)
				}
			}
			peerConfig, _, err := f.store.DesiredNodeConfig(ctx, f.nodeID)
			if err != nil {
				t.Fatal(err)
			}
			peerNode := cloneNode(f.store.nodes[f.nodeID])
			peerMaterial := cloneVLESSRuntimeMaterial(f.store.vlessRuntimeMaterials[f.materialID])
			addBrokenMemberGroup(t, f.store, f.nodeID, at)
			wake := f.store.DesiredConfigChanges(f.nodeID)
			defer f.store.ReleaseDesiredConfigChanges(f.nodeID, wake)
			switch operation {
			case "add":
				_, _, err = f.store.UpsertGroupMember(ctx, groups.Member{GroupID: f.rule.EntryGroupID, NodeID: targetID, DialHost: "192.0.2.23", Weight: 100, CreatedAt: at, UpdatedAt: at}, projectionAudit("join", at))
			case "retire":
				_, err = f.store.RetireGroupMember(ctx, f.rule.EntryGroupID, targetID, at, projectionAudit("retire", at))
			case "delete-offline":
				_, _, err = f.store.DeleteOffline(ctx, nodes.DeleteInput{NodeID: targetID, DeletedBy: "admin", IdempotencyKey: "delete", RequestSHA256: "delete-hash", DeletedAt: at, OnlineFor: time.Minute}, projectionAudit("delete", at))
			case "reenroll":
				_, err = service.Reenroll(ctx, f.store.nodes[targetID], input)
			case "heartbeat":
				node := f.store.nodes[targetID]
				capabilities := append(cloneStrings(node.Capabilities), agentv1.CapabilityUsageGeneration)
				_, err = f.store.UpdateHeartbeat(ctx, targetID, nodes.Heartbeat{Capabilities: capabilities}, at, audit.Event{})
			}
			if err != nil {
				t.Fatal("independent peer blocked member operation", err)
			}
			after, _, err := f.store.DesiredNodeConfig(ctx, f.nodeID)
			if err != nil || peerConfig.Generation != after.Generation || !bytes.Equal(peerConfig.Config, after.Config) || !reflect.DeepEqual(peerNode, f.store.nodes[f.nodeID]) {
				t.Fatal("member operation changed an independent peer", err)
			}
			if !reflect.DeepEqual(peerMaterial, f.store.vlessRuntimeMaterials[f.materialID]) {
				t.Fatal("member operation changed the peer's existing identity material")
			}
			requireNoDesiredChange(t, wake)
			if operation == "add" || operation == "reenroll" || operation == "heartbeat" {
				requireAutomaticRuleBundle(t, f, targetID)
			}
			if operation == "retire" {
				config, _, err := f.store.DesiredNodeConfig(ctx, targetID)
				var bundle agentv1.ConfigurationBundle
				if err != nil || json.Unmarshal(config.Config, &bundle) != nil || len(bundle.Fragments) != 0 {
					t.Fatal("retired node retained its old rule assignment", err)
				}
			}
		})
	}
}

func TestMemberSchedulingChangesDoNotCompileOrNotify(t *testing.T) {
	for _, operation := range []string{"weight-api", "upsert-weight", "upsert-priority"} {
		t.Run(operation, func(t *testing.T) {
			f := activeNativeMemberFixture(t)
			ctx := context.Background()
			at := f.rule.CreatedAt.Add(time.Minute)
			before, _, err := f.store.DesiredNodeConfig(ctx, f.nodeID)
			if err != nil {
				t.Fatal(err)
			}
			addBrokenMemberGroup(t, f.store, f.nodeID, at)
			wake := f.store.DesiredConfigChanges(f.nodeID)
			defer f.store.ReleaseDesiredConfigChanges(f.nodeID, wake)
			member := f.store.membersByGroup[f.rule.EntryGroupID][f.nodeID]
			if operation == "weight-api" {
				result, updateErr := f.store.UpdateGroupMemberWeight(ctx, groups.UpdateMemberWeightInput{GroupID: member.GroupID, NodeID: member.NodeID, Weight: 25, ExpectedUpdatedAt: member.UpdatedAt, UpdatedAt: at, UpdatedBy: "admin", IdempotencyKey: "weight", RequestSHA256: "weight-hash"}, projectionAudit("weight", at))
				err = updateErr
				if len(result.Assignments) != 0 {
					t.Fatal("weight edit generated engine assignments")
				}
			} else {
				if operation == "upsert-weight" {
					member.Weight = 25
				} else {
					member.Priority++
				}
				member.UpdatedAt = at
				var assignments []generations.NodeConfigGeneration
				_, assignments, err = f.store.UpsertGroupMember(ctx, member, projectionAudit("schedule", at))
				if len(assignments) != 0 {
					t.Fatal("scheduling edit generated engine assignments")
				}
			}
			if err != nil {
				t.Fatal("scheduling change invoked the engine compiler", err)
			}
			stored := f.store.membersByGroup[member.GroupID][member.NodeID]
			if operation == "upsert-priority" && stored.Priority != member.Priority || operation != "upsert-priority" && stored.Weight != 25 {
				t.Fatal("member scheduling change was not saved")
			}
			for _, candidate := range f.store.endpointMembers[f.endpointID] {
				if candidate.NodeID == f.nodeID && (candidate.Weight != stored.Weight || candidate.Priority != stored.Priority) {
					t.Fatal("endpoint scheduling projection did not follow the group")
				}
			}
			after, _, err := f.store.DesiredNodeConfig(ctx, f.nodeID)
			if err != nil || before.Generation != after.Generation || !bytes.Equal(before.Config, after.Config) {
				t.Fatal("scheduling change replaced the running configuration", err)
			}
			requireNoDesiredChange(t, wake)
		})
	}
}

func TestFailedNodeCompilationBatchDoesNotNotify(t *testing.T) {
	f := automaticMemberFixture(t)
	ctx := context.Background()
	at := f.rule.CreatedAt.Add(time.Minute)
	const brokenNode = "zz-broken-node"
	f.store.nodes[brokenNode] = nodes.Node{ID: brokenNode}
	addBrokenMemberGroup(t, f.store, brokenNode, at)
	before, _, err := f.store.DesiredNodeConfig(ctx, f.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	wake := f.store.DesiredConfigChanges(f.nodeID)
	defer f.store.ReleaseDesiredConfigChanges(f.nodeID, wake)
	// Make the first node compile successfully before the later node fails.
	rule := f.store.forwardRules[f.rule.ID]
	rule.Paused = true
	f.store.forwardRules[rule.ID] = rule
	if err := f.store.recompileNodesLocked(map[string]struct{}{f.nodeID: {}, brokenNode: {}}, at); err == nil {
		t.Fatal("fault injection did not fail the configuration batch")
	}
	after, _, err := f.store.DesiredNodeConfig(ctx, f.nodeID)
	if err != nil || before.Generation != after.Generation || !bytes.Equal(before.Config, after.Config) {
		t.Fatal("failed batch did not restore the previous configuration", err)
	}
	requireNoDesiredChange(t, wake)
}
