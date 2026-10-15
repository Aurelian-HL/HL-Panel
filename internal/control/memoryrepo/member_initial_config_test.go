package memoryrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	"github.com/hongle/hl-panel/internal/securetoken"
)

func automaticMemberFixture(t *testing.T) vlessBundleFixture {
	t.Helper()
	f := newVLESSBundleFixture(t, true)
	delete(f.store.vlessRuntimeMaterials, f.materialID)
	f.store.autoRealityKeys[f.rule.ID] = f.privateKey
	if _, _, err := f.store.compileNodeConfigLocked(f.nodeID, f.rule.CreatedAt); err != nil {
		t.Fatal(err)
	}
	return f
}

func requireAutomaticRuleBundle(t *testing.T, f vlessBundleFixture, nodeID string) int64 {
	t.Helper()
	config, _, err := f.store.DesiredNodeConfig(context.Background(), nodeID)
	if err != nil {
		t.Fatalf("new member did not receive existing rule: %v", err)
	}
	var bundle agentv1.ConfigurationBundle
	if err := json.Unmarshal(config.Config, &bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Fragments) != 1 || bundle.Fragments[0].GroupID != generations.ForwardingFragmentID(agentv1.EngineXray, f.rule.ID) {
		t.Fatalf("new member's bundle omitted existing automatic Reality rule")
	}
	if !bytes.Contains(bundle.Fragments[0].Config, []byte(f.credential)) || !bytes.Contains(bundle.Fragments[0].Config, []byte(f.privateKey)) {
		t.Fatal("new member did not receive the group's existing identity and key")
	}
	return config.Generation
}

func TestNewMemberReceivesExistingAutomaticRealityRule(t *testing.T) {
	for _, enroll := range []bool{false, true} {
		t.Run(map[bool]string{false: "administrator", true: "enrollment"}[enroll], func(t *testing.T) {
			f := automaticMemberFixture(t)
			oldConfig, _, _ := f.store.DesiredNodeConfig(context.Background(), f.nodeID)
			now := f.rule.CreatedAt.Add(time.Minute)
			capabilities := []string{"xray-vless-reality", agentv1.CapabilityUsageGeneration}
			var nodeID string
			if enroll {
				service := enrollment.NewService(f.store, func() time.Time { return now }, time.Hour)
				token, err := service.IssueForGroup(context.Background(), "admin", "new-node", f.rule.EntryGroupID, time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				identity, err := service.Enroll(context.Background(), enrollment.EnrollInput{
					RawToken: token.Token, Hostname: "new.example.test", DialHost: "192.0.2.20",
					Platform: "linux", Architecture: "amd64", AgentVersion: "test", Capabilities: capabilities,
				})
				if err != nil {
					t.Fatal(err)
				}
				node, err := f.store.NodeByCredentialHash(context.Background(), securetoken.Hash(identity.NodeCredential))
				if err != nil {
					t.Fatal(err)
				}
				nodeID = node.ID
			} else {
				nodeID = "new-node"
				f.store.nodes[nodeID] = nodes.Node{ID: nodeID, Name: nodeID, Capabilities: capabilities}
				_, assignments, err := f.store.UpsertGroupMember(context.Background(), groups.Member{
					GroupID: f.rule.EntryGroupID, NodeID: nodeID, DialHost: "192.0.2.20", Weight: 100, CreatedAt: now, UpdatedAt: now,
				}, projectionAudit("add-new", now))
				if err != nil || len(assignments) != 1 {
					t.Fatalf("assignments=%d err=%v", len(assignments), err)
				}
			}
			requireAutomaticRuleBundle(t, f, nodeID)
			after, _, _ := f.store.DesiredNodeConfig(context.Background(), f.nodeID)
			if after.Generation != oldConfig.Generation || !bytes.Equal(after.Config, oldConfig.Config) {
				t.Fatal("joining a new node changed the existing node's configuration")
			}
			if len(f.store.protocolHealth[f.endpointID]) != 0 {
				t.Fatal("joining a node granted unverified protocol health")
			}
		})
	}
}

func TestHeartbeatRecoversMissingConfigAndLateVLESSCapability(t *testing.T) {
	for _, lateCapability := range []bool{false, true} {
		t.Run(map[bool]string{false: "stuck-initial-config", true: "late-capability"}[lateCapability], func(t *testing.T) {
			f := automaticMemberFixture(t)
			now := f.rule.CreatedAt.Add(time.Minute)
			id := "recover-node"
			capabilities := []string{"xray-vless-reality", agentv1.CapabilityUsageGeneration}
			previousCapabilities := capabilities
			if lateCapability {
				previousCapabilities = []string{agentv1.CapabilityUsageGeneration}
			}
			f.store.nodes[id] = nodes.Node{ID: id, Name: id, Capabilities: previousCapabilities}
			m := groups.Member{GroupID: f.rule.EntryGroupID, NodeID: id, DialHost: "192.0.2.21", Weight: 100, CreatedAt: now, UpdatedAt: now}
			f.store.membersByGroup[m.GroupID][id] = m
			f.store.syncGroupMemberToEndpointPoolsLocked(m)
			if lateCapability {
				// An existing non-VLESS bundle must also reconcile capability changes.
				f.store.nodes[id] = nodes.Node{ID: id, Name: id, Capabilities: previousCapabilities, DesiredGeneration: 1}
				f.store.nodeConfigsByNode[id] = map[int64]generations.NodeConfigGeneration{1: {NodeID: id, Generation: 1, Config: json.RawMessage(`{"schema_version":1,"fragments":[]}`)}}
			}
			h := nodes.Heartbeat{Capabilities: capabilities}
			if _, err := f.store.UpdateHeartbeat(context.Background(), id, h, now, projectionAudit("recover", now)); err != nil {
				t.Fatal(err)
			}
			generation := requireAutomaticRuleBundle(t, f, id)
			if _, err := f.store.UpdateHeartbeat(context.Background(), id, h, now.Add(time.Second), projectionAudit("repeat", now)); err != nil {
				t.Fatal(err)
			}
			if got := requireAutomaticRuleBundle(t, f, id); got != generation {
				t.Fatal("ordinary heartbeat reissued configuration")
			}
		})
	}
}

func TestNewMemberCompilationFailureRestoresEndpointProjection(t *testing.T) {
	f := automaticMemberFixture(t)
	now := f.rule.CreatedAt.Add(time.Minute)
	id := "invalid-new-node"
	f.store.nodes[id] = nodes.Node{ID: id, Name: id, Capabilities: []string{"xray-vless-reality"}}
	f.store.revisionsByGroup[f.rule.EntryGroupID] = []generations.GroupRevision{{ID: "bad", GroupID: f.rule.EntryGroupID, Revision: 1, Engine: agentv1.EngineGOST, Config: json.RawMessage(`{broken`), CreatedAt: now}}
	beforeMembers := len(f.store.endpointMembers[f.endpointID])
	beforePool := f.store.endpointPools[f.endpointID]
	_, _, err := f.store.UpsertGroupMember(context.Background(), groups.Member{GroupID: f.rule.EntryGroupID, NodeID: id, Weight: 100, CreatedAt: now, UpdatedAt: now}, projectionAudit("failed-add", now))
	if err == nil {
		t.Fatal("invalid configuration accepted")
	}
	if _, joined := f.store.membersByGroup[f.rule.EntryGroupID][id]; joined {
		t.Fatal("failed add retained membership")
	}
	if len(f.store.endpointMembers[f.endpointID]) != beforeMembers || f.store.endpointPools[f.endpointID] != beforePool || f.store.nodes[id].DesiredGeneration != 0 {
		t.Fatal("failed add changed endpoint or node configuration")
	}
}

func TestInitialConfigFailurePreservesHeartbeatAndRecovers(t *testing.T) {
	f := automaticMemberFixture(t)
	now := f.rule.CreatedAt.Add(time.Minute)
	id := "pending-node"
	capabilities := []string{"xray-vless-reality"}
	f.store.nodes[id] = nodes.Node{ID: id, Name: id, Capabilities: capabilities}
	m := groups.Member{GroupID: f.rule.EntryGroupID, NodeID: id, Weight: 100, CreatedAt: now, UpdatedAt: now}
	f.store.membersByGroup[m.GroupID][id] = m
	f.store.syncGroupMemberToEndpointPoolsLocked(m)
	f.store.revisionsByGroup[m.GroupID] = []generations.GroupRevision{{ID: "bad", GroupID: m.GroupID, Revision: 1, Engine: agentv1.EngineGOST, Config: json.RawMessage(`{broken`), CreatedAt: now}}
	h := nodes.Heartbeat{Capabilities: capabilities, Resources: map[string]any{"sample": "current"}}
	node, err := f.store.UpdateHeartbeat(context.Background(), id, h, now, projectionAudit("pending", now))
	if err != nil || node.LastHeartbeatAt == nil || !node.LastHeartbeatAt.Equal(now) || node.Resources["sample"] != "current" || node.DesiredGeneration != 0 {
		t.Fatalf("failed initial compilation discarded heartbeat: %v", err)
	}
	if len(f.store.nodeConfigsByNode[id]) != 0 || len(f.store.protocolHealth[f.endpointID]) != 0 {
		t.Fatal("invalid initial configuration was published or authorized")
	}
	delete(f.store.revisionsByGroup, m.GroupID)
	if _, err := f.store.UpdateHeartbeat(context.Background(), id, h, now.Add(time.Second), projectionAudit("repaired", now)); err != nil {
		t.Fatal(err)
	}
	requireAutomaticRuleBundle(t, f, id)
}

func TestReenrollmentReceivesTargetGroupAutomaticRealityRule(t *testing.T) {
	f := automaticMemberFixture(t)
	ctx := context.Background()
	now := f.rule.CreatedAt.Add(time.Minute)
	oldGroup := f.store.deviceGroups[f.rule.EntryGroupID]
	oldGroup.ID, oldGroup.Name = "old-group", "old-group"
	if err := f.store.CreateDeviceGroup(ctx, oldGroup, projectionAudit("old-group", now)); err != nil {
		t.Fatal(err)
	}
	service := enrollment.NewService(f.store, func() time.Time { return now }, time.Hour)
	oldToken, err := service.IssueForGroup(ctx, "admin", "moving-node", oldGroup.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	input := enrollment.EnrollInput{RawToken: oldToken.Token, Hostname: "moving.example.test", DialHost: "192.0.2.22", Platform: "linux", Architecture: "amd64", AgentVersion: "test", Capabilities: []string{"xray-vless-reality"}}
	identity, err := service.Enroll(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	node, err := f.store.NodeByCredentialHash(ctx, securetoken.Hash(identity.NodeCredential))
	if err != nil {
		t.Fatal(err)
	}
	nextToken, err := service.IssueForGroup(ctx, "admin", "moving-node", f.rule.EntryGroupID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	input.RawToken = nextToken.Token
	moved, err := service.Reenroll(ctx, node, input)
	if err != nil {
		t.Fatal(err)
	}
	requireAutomaticRuleBundle(t, f, moved.ID)
	if f.store.membersByGroup[oldGroup.ID][moved.ID].RetiredAt == nil {
		t.Fatal("old group membership remained active")
	}
}
