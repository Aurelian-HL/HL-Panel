package memoryrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"reflect"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/forwarding/gostconfig"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	"github.com/hongle/hl-panel/internal/securetoken"
)

func nativeReenrollmentFixture(t *testing.T) (vlessBundleFixture, *enrollment.Service, enrollment.EnrollInput, string) {
	t.Helper()
	f := activeNativeMemberFixture(t)
	// Match enrolled nodes' non-nil telemetry maps before checking rollback.
	for id, node := range f.store.nodes {
		f.store.nodes[id] = cloneNode(node)
	}
	ctx := context.Background()
	now := f.rule.CreatedAt.Add(time.Minute)
	previousGroup := f.store.deviceGroups[f.rule.EntryGroupID]
	previousGroup.ID, previousGroup.Name = "previous-group", "previous-group"
	if err := f.store.CreateDeviceGroup(ctx, previousGroup, projectionAudit("previous-group", now)); err != nil {
		t.Fatal(err)
	}
	service := enrollment.NewService(f.store, func() time.Time { return now }, time.Hour)
	previousToken, err := service.IssueForGroup(ctx, "admin", "moving-node", previousGroup.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	input := enrollment.EnrollInput{RawToken: previousToken.Token, Hostname: "moving.example.test", DialHost: "192.0.2.23",
		Platform: "linux", Architecture: "amd64", AgentVersion: "test", Capabilities: f.store.nodes[f.nodeID].Capabilities}
	identity, err := service.Enroll(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	targetToken, err := service.IssueForGroup(ctx, "admin", "moving-node", f.rule.EntryGroupID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	input.RawToken = targetToken.Token
	return f, service, input, identity.NodeID
}

func TestReenrollmentReceivesImportedGroupRules(t *testing.T) {
	f, service, input, nodeID := nativeReenrollmentFixture(t)
	ctx := context.Background()
	before, _, err := f.store.DesiredNodeConfig(ctx, f.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	moved, err := service.Reenroll(ctx, f.store.nodes[nodeID], input)
	if err != nil {
		t.Fatal(err)
	}
	requireAutomaticRuleBundle(t, f, nodeID)
	material, err := f.store.RuntimeMaterialForNode(ctx, nodeID, f.bindingID)
	if err != nil || material.ID == f.materialID || material.NodeID != nodeID {
		t.Fatal("reinstalled member did not receive independent imported material", err)
	}
	after, _, err := f.store.DesiredNodeConfig(ctx, f.nodeID)
	if err != nil || before.Generation != after.Generation || !bytes.Equal(before.Config, after.Config) {
		t.Fatal("moving a node changed an unaffected member's configuration", err)
	}
	raw, err := f.store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal("reinstalled group cannot be restored", err)
	}
	if _, err := restored.RuntimeMaterialForNode(ctx, nodeID, f.bindingID); err != nil {
		t.Fatal("imported material did not survive restore", err)
	}
	if _, err := service.Reenroll(ctx, moved, input); err != nil {
		t.Fatal(err)
	}
	replayed, err := f.store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	assertUDPSnapshotUnchanged(t, raw, replayed)
}

func TestReenrollmentNativeFailureIsAtomicAndRetryable(t *testing.T) {
	for _, failure := range []string{"invalid-source", "invalid-revision"} {
		t.Run(failure, func(t *testing.T) {
			f, service, input, nodeID := nativeReenrollmentFixture(t)
			before, err := f.store.EncodeSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			material := cloneVLESSRuntimeMaterial(f.store.vlessRuntimeMaterials[f.materialID])
			revisions, hadRevisions := f.store.revisionsByGroup[f.rule.EntryGroupID]
			if failure == "invalid-source" {
				corrupted := cloneVLESSRuntimeMaterial(material)
				corrupted.NativeConfig = json.RawMessage(`{broken`)
				f.store.vlessRuntimeMaterials[material.ID] = corrupted
			} else {
				f.store.revisionsByGroup[f.rule.EntryGroupID] = []generations.GroupRevision{{GroupID: f.rule.EntryGroupID, Revision: 1, Engine: agentv1.EngineGOST, Config: json.RawMessage(`{broken`)}}
			}
			if _, err := service.Reenroll(context.Background(), f.store.nodes[nodeID], input); err == nil {
				t.Error("invalid imported group was accepted")
			}
			f.store.vlessRuntimeMaterials[material.ID] = material
			if hadRevisions {
				f.store.revisionsByGroup[f.rule.EntryGroupID] = revisions
			} else {
				delete(f.store.revisionsByGroup, f.rule.EntryGroupID)
			}
			after, err := f.store.EncodeSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			assertUDPSnapshotUnchanged(t, before, after)
			if t.Failed() {
				return
			}
			if _, err := service.Reenroll(context.Background(), f.store.nodes[nodeID], input); err != nil {
				t.Fatal("same token could not retry", err)
			}
			requireAutomaticRuleBundle(t, f, nodeID)
		})
	}
}

func requireUDPTunnelHosts(t *testing.T, s *Store, rule forwarding.Rule, hosts ...string) {
	t.Helper()
	config, exists := s.nodeConfigsByNode["entry-node"][s.nodes["entry-node"].DesiredGeneration]
	if !exists {
		t.Fatal("entry has no assigned configuration")
	}
	var bundle agentv1.ConfigurationBundle
	if err := json.Unmarshal(config.Config, &bundle); err != nil {
		t.Fatal(err)
	}
	for _, fragment := range bundle.Fragments {
		if fragment.GroupID != generations.ForwardingFragmentID(agentv1.EngineGOST, rule.ID) {
			continue
		}
		var tunnel gostconfig.Configuration
		if err := json.Unmarshal(fragment.Config, &tunnel); err != nil {
			t.Fatal(err)
		}
		if len(tunnel.Chains) != 1 || len(tunnel.Chains[0].Hops) != 1 {
			t.Fatal("missing entry tunnel chain")
		}
		got := []string{}
		for _, node := range tunnel.Chains[0].Hops[0].Nodes {
			got = append(got, node.Addr)
		}
		want := make([]string, len(hosts))
		for i, host := range hosts {
			want[i] = net.JoinHostPort(host, strconv.Itoa(s.reliableUDPTunnels[rule.ID].Port))
		}
		sort.Strings(got)
		sort.Strings(want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("entry tunnel retained stale exits: got=%v want=%v", got, want)
		}
		return
	}
	t.Fatal("entry tunnel fragment missing")
}

func TestReenrollmentRecompilesReliableUDPDependencies(t *testing.T) {
	for _, transport := range []forwarding.TunnelTransport{forwarding.TunnelQUIC, forwarding.TunnelKCP} {
		for _, change := range []string{"same-group-address", "move-group", "dependent-failure"} {
			t.Run(string(transport)+"/"+change, func(t *testing.T) {
				s, _, req, rule := reliableUDPFixture(t, transport)
				ctx := context.Background()
				now := time.Now().UTC()
				peer := cloneNode(s.nodes["exit-node"])
				peer.ID, peer.CredentialHash = "exit-peer", securetoken.Hash("udp-peer")
				s.nodes[peer.ID], s.nodesByCredential[peer.CredentialHash] = peer, peer.ID
				if _, _, err := s.UpsertGroupMember(ctx, groups.Member{GroupID: req.ExitGroupID, NodeID: peer.ID, DialHost: "127.0.0.2", Weight: 1, CreatedAt: now, UpdatedAt: now}, projectionAudit("join-peer", now)); err != nil {
					t.Fatal(err)
				}
				acknowledgeUDPConfig(t, s, "exit-node")
				acknowledgeUDPConfig(t, s, peer.ID)
				acknowledgeUDPConfig(t, s, "entry-node")
				requireUDPTunnelHosts(t, s, rule, "exit.example.test", "127.0.0.2")
				peerConfig := cloneNodeConfig(s.nodeConfigsByNode[peer.ID][s.nodes[peer.ID].DesiredGeneration])
				groupID := req.ExitGroupID
				if change == "move-group" {
					group := s.deviceGroups[req.ExitGroupID]
					group.ID, group.Name = "new-exit-group", "new-exit-group"
					if err := s.CreateDeviceGroup(ctx, group, projectionAudit("new-exit-group", now)); err != nil {
						t.Fatal(err)
					}
					groupID = group.ID
				}
				service := enrollment.NewService(s, func() time.Time { return now }, time.Hour)
				token, err := service.IssueForGroup(ctx, "admin", "reinstalled-exit", groupID, time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				input := enrollment.EnrollInput{RawToken: token.Token, Hostname: "reinstalled.example.test", DialHost: "127.0.0.3", Platform: "linux", Architecture: "amd64", AgentVersion: "test"}
				if change == "dependent-failure" {
					wakes := make(map[string]<-chan struct{})
					for _, id := range []string{"entry-node", "exit-node", peer.ID} {
						wakes[id] = s.DesiredConfigChanges(id)
						defer s.ReleaseDesiredConfigChanges(id, wakes[id])
					}
					before, err := s.EncodeSnapshot()
					if err != nil {
						t.Fatal(err)
					}
					previous, hadRevision := s.revisionsByGroup[req.EntryGroupID]
					s.revisionsByGroup[req.EntryGroupID] = []generations.GroupRevision{{GroupID: req.EntryGroupID, Revision: 1, Engine: agentv1.EngineGOST, Config: json.RawMessage(`{broken`)}}
					if _, err := service.Reenroll(ctx, s.nodes["exit-node"], input); err == nil {
						t.Error("reinstall ignored dependent compilation failure")
					}
					for _, wake := range wakes {
						requireNoDesiredChange(t, wake)
					}
					if hadRevision {
						s.revisionsByGroup[req.EntryGroupID] = previous
					} else {
						delete(s.revisionsByGroup, req.EntryGroupID)
					}
					after, err := s.EncodeSnapshot()
					if err != nil {
						t.Fatal(err)
					}
					assertUDPSnapshotUnchanged(t, before, after)
					if t.Failed() {
						return
					}
				}
				moved, err := service.Reenroll(ctx, s.nodes["exit-node"], input)
				if err != nil {
					t.Fatal(err)
				}
				if change == "move-group" {
					requireUDPTunnelHosts(t, s, rule, "127.0.0.2")
				} else {
					requireUDPTunnelHosts(t, s, rule, "127.0.0.2", "127.0.0.3")
				}
				afterPeer := s.nodeConfigsByNode[peer.ID][s.nodes[peer.ID].DesiredGeneration]
				if peerConfig.Generation != afterPeer.Generation || !bytes.Equal(peerConfig.Config, afterPeer.Config) {
					t.Fatal("reinstall changed the surviving exit's configuration")
				}
				raw, err := s.EncodeSnapshot()
				if err != nil {
					t.Fatal(err)
				}
				if _, err := DecodeSnapshot(raw); err != nil {
					t.Fatal("reinstalled UDP state cannot be restored", err)
				}
				if _, err := service.Reenroll(ctx, moved, input); err != nil {
					t.Fatal(err)
				}
				replayed, err := s.EncodeSnapshot()
				if err != nil {
					t.Fatal(err)
				}
				assertUDPSnapshotUnchanged(t, raw, replayed)
			})
		}
	}
}
