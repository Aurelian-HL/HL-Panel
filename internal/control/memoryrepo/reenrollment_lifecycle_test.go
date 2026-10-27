package memoryrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	"github.com/hongle/hl-panel/internal/securetoken"
)

func TestReenrollmentPreservesNativeIdentityAfterRetirement(t *testing.T) {
	for _, lifecycle := range []string{"retired", "deleted", "revoked"} {
		t.Run(lifecycle, func(t *testing.T) {
			f, service, input, nodeID := nativeReenrollmentFixture(t)
			ctx := context.Background()
			if _, err := service.Reenroll(ctx, f.store.nodes[nodeID], input); err != nil {
				t.Fatal(err)
			}
			original, err := f.store.RuntimeMaterialForNode(ctx, nodeID, f.bindingID)
			if err != nil {
				t.Fatal(err)
			}
			peerBefore, _, err := f.store.DesiredNodeConfig(ctx, f.nodeID)
			if err != nil {
				t.Fatal(err)
			}
			beforeNode := cloneNode(f.store.nodes[nodeID])
			at := f.rule.CreatedAt.Add(2 * time.Minute)
			if _, err := f.store.RetireGroupMember(ctx, f.rule.EntryGroupID, nodeID, at, projectionAudit("retire-reinstalled", at)); err != nil {
				t.Fatal(err)
			}
			if lifecycle == "deleted" {
				if _, _, err := f.store.DeleteOffline(ctx, nodes.DeleteInput{NodeID: nodeID, DeletedBy: "admin", IdempotencyKey: "delete-reinstalled", RequestSHA256: "delete-reinstalled-hash", DeletedAt: at.Add(time.Second), OnlineFor: time.Minute}, projectionAudit("delete-reinstalled", at)); err != nil {
					t.Fatal(err)
				}
			}
			if lifecycle == "revoked" {
				runtime, err := vlessruntime.NewService(f.store, func() time.Time { return at })
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := runtime.Revoke(ctx, "admin", original.ID, original.Revision, "revoke-reinstalled"); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := f.store.EncodeSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			f.store, err = DecodeSnapshot(raw)
			if err != nil {
				t.Fatal("inactive reinstall identity cannot be restored", err)
			}
			service = enrollment.NewService(f.store, func() time.Time { return at.Add(time.Minute) }, time.Hour)
			token, err := service.IssueForGroup(ctx, "admin", "rejoined-native", f.rule.EntryGroupID, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			input.RawToken, input.Capabilities = token.Token, nil
			moved, err := service.Reenroll(ctx, f.store.nodes[nodeID], input)
			if err != nil {
				t.Fatal(err)
			}
			if moved.DeletedAt != nil || moved.ID != beforeNode.ID || moved.CredentialHash != beforeNode.CredentialHash || !reflect.DeepEqual(moved.Capabilities, beforeNode.Capabilities) {
				t.Fatal("reinstall lost the durable identity or previously negotiated capabilities")
			}
			material, err := f.store.RuntimeMaterialForNode(ctx, nodeID, f.bindingID)
			if lifecycle == "revoked" {
				if !errors.Is(err, faults.ErrNotFound) || material.ID != "" || f.store.vlessRuntimeMaterials[original.ID].State != vlessruntime.StateRevoked {
					t.Fatal("reinstall revived explicitly revoked runtime credentials")
				}
			} else {
				if err != nil || !reflect.DeepEqual(original, material) {
					t.Fatal("reinstall changed the existing native identity", err)
				}
				requireAutomaticRuleBundle(t, f, nodeID)
			}
			peerAfter, _, err := f.store.DesiredNodeConfig(ctx, f.nodeID)
			if err != nil || peerBefore.Generation != peerAfter.Generation || !bytes.Equal(peerBefore.Config, peerAfter.Config) {
				t.Fatal("reinstall changed the surviving peer's bundle", err)
			}
			raw, err = f.store.EncodeSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeSnapshot(raw); err != nil {
				t.Fatal("rejoined native state cannot be restored", err)
			}
		})
	}
}

func TestNativeGroupReplacementWithoutLiveSourcePreservesImportedRules(t *testing.T) {
	for _, lifecycle := range []string{"retired", "deleted"} {
		for _, registration := range []string{"enroll", "reenroll"} {
			t.Run(lifecycle+"/"+registration, func(t *testing.T) {
				f, _, input, nodeID := nativeReenrollmentFixture(t)
				ctx, at := context.Background(), f.rule.CreatedAt.Add(2*time.Minute)
				original := cloneVLESSRuntimeMaterial(f.store.vlessRuntimeMaterials[f.materialID])
				if _, err := f.store.RetireGroupMember(ctx, f.rule.EntryGroupID, f.nodeID, at, projectionAudit("retire-last-source", at)); err != nil {
					t.Fatal(err)
				}
				if lifecycle == "deleted" {
					if _, _, err := f.store.DeleteOffline(ctx, nodes.DeleteInput{NodeID: f.nodeID, DeletedBy: "admin", IdempotencyKey: "delete-last-source", RequestSHA256: "delete-last-source-hash", DeletedAt: at.Add(time.Second), OnlineFor: time.Minute}, projectionAudit("delete-last-source", at)); err != nil {
						t.Fatal(err)
					}
				}
				raw, err := f.store.EncodeSnapshot()
				if err != nil {
					t.Fatal(err)
				}
				f.store, err = DecodeSnapshot(raw)
				if err != nil {
					t.Fatal(err)
				}
				service := enrollment.NewService(f.store, func() time.Time { return at.Add(time.Minute) }, time.Hour)
				if registration == "enroll" {
					identity, err := service.Enroll(ctx, input)
					if err != nil {
						t.Fatal(err)
					}
					nodeID = identity.NodeID
				} else if _, err := service.Reenroll(ctx, f.store.nodes[nodeID], input); err != nil {
					t.Fatal(err)
				}
				material, err := f.store.RuntimeMaterialForNode(ctx, nodeID, f.bindingID)
				if err != nil || material.ID == original.ID || material.CredentialUUID != original.CredentialUUID ||
					material.RealityPrivateKey != original.RealityPrivateKey || !bytes.Equal(material.NativeConfig, original.NativeConfig) {
					t.Fatal("replacement silently lost the group's imported rule", err)
				}
				requireAutomaticRuleBundle(t, f, nodeID)
				if _, err := f.store.RuntimeMaterialForNode(ctx, f.nodeID, f.bindingID); !errors.Is(err, faults.ErrNotFound) || f.store.membersByGroup[f.rule.EntryGroupID][f.nodeID].RetiredAt == nil {
					t.Fatal("replacement reauthorized the inactive source", err)
				}
				raw, err = f.store.EncodeSnapshot()
				if err != nil {
					t.Fatal(err)
				}
				if _, err := DecodeSnapshot(raw); err != nil {
					t.Fatal("replacement cannot be restored", err)
				}
			})
		}
	}
}

func TestReenrollmentWithdrawsSoleUDPExitAndRestoresOverlappingGroups(t *testing.T) {
	for _, transport := range []forwarding.TunnelTransport{forwarding.TunnelQUIC, forwarding.TunnelKCP} {
		for _, shape := range []string{"one-old-group", "two-old-groups"} {
			t.Run(string(transport)+"/"+shape, func(t *testing.T) {
				s, svc, req, rule := reliableUDPFixture(t, transport)
				ctx, now := context.Background(), time.Now().UTC()
				rules := []forwarding.Rule{rule}
				oldGroups := []string{req.ExitGroupID}
				if shape == "two-old-groups" {
					group := s.deviceGroups[req.ExitGroupID]
					group.ID, group.Name = "second-exit-group", "second-exit-group"
					if err := s.CreateDeviceGroup(ctx, group, projectionAudit("second-exit", now)); err != nil {
						t.Fatal(err)
					}
					s.groupNetworks[group.ID] = s.groupNetworks[req.ExitGroupID]
					network := s.groupNetworks[group.ID]
					network.GroupID = group.ID
					s.groupNetworks[group.ID] = network
					entryNetwork := s.groupNetworks[req.EntryGroupID]
					entryNetwork.AllowedExitGroupIDs = append(append([]string(nil), entryNetwork.AllowedExitGroupIDs...), group.ID)
					s.groupNetworks[req.EntryGroupID] = entryNetwork
					userGroupID := s.customers[req.CustomerID].UserGroupID
					userGroup := s.userGroups[userGroupID]
					userGroup.AllowedExitGroupIDs = append(append([]string(nil), userGroup.AllowedExitGroupIDs...), group.ID)
					s.userGroups[userGroupID] = userGroup
					if _, _, err := s.UpsertGroupMember(ctx, groups.Member{GroupID: group.ID, NodeID: "exit-node", DialHost: "exit.example.test", Weight: 1, CreatedAt: now, UpdatedAt: now}, projectionAudit("shared-exit", now)); err != nil {
						t.Fatal(err)
					}
					req.ExitGroupID, req.Name = group.ID, "second-udp"
					other, _, err := svc.Create(ctx, "admin", req, "second-udp")
					if err != nil {
						t.Fatal(err)
					}
					rules, oldGroups = append(rules, other), append(oldGroups, group.ID)
				}
				acknowledgeUDPConfig(t, s, "exit-node")
				acknowledgeUDPConfig(t, s, "entry-node")
				for _, r := range rules {
					requireUDPTunnelHosts(t, s, r, "exit.example.test")
				}
				latePeer := cloneNode(s.nodes["entry-node"])
				latePeer.ID, latePeer.CredentialHash = "zz-last-entry", securetoken.Hash("late-reenroll-peer")
				s.nodes[latePeer.ID], s.nodesByCredential[latePeer.CredentialHash] = latePeer, latePeer.ID
				if _, _, err := s.UpsertGroupMember(ctx, groups.Member{GroupID: req.EntryGroupID, NodeID: latePeer.ID, DialHost: "127.0.0.4", Weight: 1, CreatedAt: now, UpdatedAt: now}, projectionAudit("late-peer", now)); err != nil {
					t.Fatal(err)
				}
				extra := s.deviceGroups[req.EntryGroupID]
				extra.ID, extra.Name = "late-peer-only-group", "late-peer-only-group"
				if err := s.CreateDeviceGroup(ctx, extra, projectionAudit("late-only", now)); err != nil {
					t.Fatal(err)
				}
				if _, _, err := s.UpsertGroupMember(ctx, groups.Member{GroupID: extra.ID, NodeID: latePeer.ID, DialHost: "127.0.0.4", Weight: 1, CreatedAt: now, UpdatedAt: now}, projectionAudit("extra-group", now)); err != nil {
					t.Fatal(err)
				}
				target := s.deviceGroups[req.ExitGroupID]
				target.ID, target.Name = "target-exit-group", "target-exit-group"
				if err := s.CreateDeviceGroup(ctx, target, projectionAudit("target-exit", now)); err != nil {
					t.Fatal(err)
				}
				service := enrollment.NewService(s, func() time.Time { return now }, time.Hour)
				token, err := service.IssueForGroup(ctx, "admin", "moved-exit", target.ID, time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				input := enrollment.EnrollInput{RawToken: token.Token, Hostname: "moved.example.test", DialHost: "127.0.0.5", Platform: "linux", Architecture: "amd64", AgentVersion: "test"}
				before, err := s.EncodeSnapshot()
				if err != nil {
					t.Fatal(err)
				}
				// Only the final sorted dependent node fails, after the moving node
				// and an earlier entry have already compiled their candidate bundles.
				wakes := make(map[string]<-chan struct{})
				for _, id := range []string{"entry-node", "exit-node", latePeer.ID} {
					wakes[id] = s.DesiredConfigChanges(id)
					defer s.ReleaseDesiredConfigChanges(id, wakes[id])
				}
				s.revisionsByGroup[extra.ID] = []generations.GroupRevision{{GroupID: extra.ID, Revision: 1, Engine: agentv1.EngineGOST, Config: json.RawMessage(`{broken`)}}
				if _, err := service.Reenroll(ctx, s.nodes["exit-node"], input); err == nil {
					t.Fatal("late dependent compilation failure was ignored")
				}
				for _, wake := range wakes {
					requireNoDesiredChange(t, wake)
				}
				delete(s.revisionsByGroup, extra.ID)
				after, err := s.EncodeSnapshot()
				if err != nil {
					t.Fatal(err)
				}
				assertUDPSnapshotUnchanged(t, before, after)
				if t.Failed() {
					return
				}
				if _, err := DecodeSnapshot(after); err != nil {
					t.Fatal("rollback left an unreadable snapshot", err)
				}
				if _, err := service.Reenroll(ctx, s.nodes["exit-node"], input); err != nil {
					t.Fatal("failed reinstall consumed its retry token", err)
				}
				for _, groupID := range oldGroups {
					if s.membersByGroup[groupID]["exit-node"].RetiredAt == nil {
						t.Fatal("reinstall left an old exit membership active")
					}
				}
				for _, id := range []string{"entry-node", latePeer.ID, "exit-node"} {
					config := s.nodeConfigsByNode[id][s.nodes[id].DesiredGeneration]
					var bundle agentv1.ConfigurationBundle
					if err := json.Unmarshal(config.Config, &bundle); err != nil {
						t.Fatal(err)
					}
					for _, fragment := range bundle.Fragments {
						for _, r := range rules {
							if fragment.GroupID == generations.ForwardingFragmentID(agentv1.EngineGOST, r.ID) {
								t.Fatal("old UDP path remained after its only exit left the group")
							}
						}
					}
				}
				raw, err := s.EncodeSnapshot()
				if err != nil {
					t.Fatal(err)
				}
				if _, err := DecodeSnapshot(raw); err != nil {
					t.Fatal("successful multi-group reinstall cannot be restored", err)
				}
			})
		}
	}
}
