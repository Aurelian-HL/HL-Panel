package memoryrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	agentconfig "github.com/hongle/hl-panel/internal/agent/config"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	"github.com/hongle/hl-panel/internal/securetoken"
)

func activeNativeMemberFixture(t *testing.T) vlessBundleFixture {
	t.Helper()
	f, profile := nativeRuntimeFixture(t)
	service, _ := vlessruntime.NewService(f.store, time.Now)
	material, _, err := service.BindImported(context.Background(), "admin", f.bindingID, f.nodeID, profile.Config, "import")
	if err != nil {
		t.Fatal(err)
	}
	f.materialID, f.credential, f.privateKey = material.ID, profile.UUID, profile.PrivateKey
	f.rule.Paused = false
	f.store.forwardRules[f.rule.ID] = f.rule
	if _, _, err := f.store.compileNodeConfigLocked(f.nodeID, f.rule.CreatedAt); err != nil {
		t.Fatal(err)
	}
	return f
}

func nativeJoiningMember(f vlessBundleFixture) groups.Member {
	return groups.Member{GroupID: f.rule.EntryGroupID, NodeID: "native-new-node", DialHost: "192.0.2.23", Weight: 100,
		CreatedAt: f.rule.CreatedAt.Add(time.Minute), UpdatedAt: f.rule.CreatedAt.Add(time.Minute)}
}

func TestNewMemberReceivesImportedGroupRules(t *testing.T) {
	for _, enroll := range []bool{false, true} {
		t.Run(map[bool]string{false: "administrator", true: "enrollment"}[enroll], func(t *testing.T) {
			f := activeNativeMemberFixture(t)
			before, _, _ := f.store.DesiredNodeConfig(context.Background(), f.nodeID)
			member := nativeJoiningMember(f)
			capabilities := f.store.nodes[f.nodeID].Capabilities
			if enroll {
				service := enrollment.NewService(f.store, func() time.Time { return member.UpdatedAt }, time.Hour)
				token, err := service.IssueForGroup(context.Background(), "admin", "new-node", member.GroupID, time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				identity, err := service.Enroll(context.Background(), enrollment.EnrollInput{RawToken: token.Token,
					Hostname: "native.example.test", DialHost: member.DialHost, Platform: "linux", Architecture: "amd64", AgentVersion: "test", Capabilities: capabilities})
				if err != nil {
					t.Fatal(err)
				}
				node, err := f.store.NodeByCredentialHash(context.Background(), securetoken.Hash(identity.NodeCredential))
				if err != nil {
					t.Fatal(err)
				}
				member.NodeID = node.ID
			} else {
				node := cloneNode(f.store.nodes[f.nodeID])
				node.ID, node.CredentialHash = member.NodeID, securetoken.Hash("synthetic-new-node")
				f.store.nodes[member.NodeID] = node
				f.store.nodesByCredential[node.CredentialHash] = node.ID
				if _, _, err := f.store.UpsertGroupMember(context.Background(), member, projectionAudit("join-native", member.UpdatedAt)); err != nil {
					t.Fatal(err)
				}
			}
			requireAutomaticRuleBundle(t, f, member.NodeID)
			after, _, _ := f.store.DesiredNodeConfig(context.Background(), f.nodeID)
			if before.Generation != after.Generation || !bytes.Equal(before.Config, after.Config) {
				t.Fatal("provisioning a member changed an existing node's bundle")
			}
			material, err := f.store.RuntimeMaterialForNode(context.Background(), member.NodeID, f.bindingID)
			if err != nil || material.ID == f.materialID || material.NodeID != member.NodeID || material.Revision != 1 {
				t.Fatal("member did not get an independently revocable material", err)
			}
			raw, err := f.store.EncodeSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			restored, err := DecodeSnapshot(raw)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := restored.RuntimeMaterialForNode(context.Background(), member.NodeID, f.bindingID); err != nil {
				t.Fatal("material lost after persistence", err)
			}
			material.NativeConfig[0] = '!'
			if _, err := f.store.RuntimeMaterialForNode(context.Background(), f.nodeID, f.bindingID); err != nil {
				t.Fatal("provisioning aliased source bytes", err)
			}
		})
	}
}

func TestExistingIncompleteMemberCanBeReconciledIdempotently(t *testing.T) {
	f := activeNativeMemberFixture(t)
	member := nativeJoiningMember(f)
	f.store.nodes[member.NodeID] = nodes.Node{ID: member.NodeID, Capabilities: f.store.nodes[f.nodeID].Capabilities}
	f.store.membersByGroup[member.GroupID][member.NodeID] = member
	f.store.syncGroupMemberToEndpointPoolsLocked(member)
	for attempt := 0; attempt < 2; attempt++ {
		count := len(f.store.vlessRuntimeMaterials)
		_, assignments, err := f.store.UpsertGroupMember(context.Background(), member, projectionAudit("repair", member.UpdatedAt))
		if err != nil {
			t.Fatal(err)
		}
		requireAutomaticRuleBundle(t, f, member.NodeID)
		if attempt == 0 && (len(assignments) != 1 || len(f.store.vlessRuntimeMaterials) != count+1) {
			t.Fatal("existing membership was not repaired")
		}
		if attempt == 1 && (len(assignments) != 0 || len(f.store.vlessRuntimeMaterials) != count) {
			t.Fatal("repeated repair reissued material/configuration")
		}
	}
}

func TestNativeMemberProvisioningPreservesRevocationAndRollsBack(t *testing.T) {
	for _, failure := range []string{"revoked", "invalid-source", "ambiguous-source", "compile-failure"} {
		t.Run(failure, func(t *testing.T) {
			f := activeNativeMemberFixture(t)
			member := nativeJoiningMember(f)
			f.store.nodes[member.NodeID] = nodes.Node{ID: member.NodeID, Capabilities: f.store.nodes[f.nodeID].Capabilities}
			switch failure {
			case "revoked":
				m := cloneVLESSRuntimeMaterial(f.store.vlessRuntimeMaterials[f.materialID])
				m.ID, m.NodeID, m.State = "revoked-local", member.NodeID, vlessruntime.StateRevoked
				m.CredentialUUID, m.RealityPrivateKey, m.NativeConfig = "", "", nil
				m.RevokedAt = &member.UpdatedAt
				f.store.vlessRuntimeMaterials[m.ID] = m
			case "invalid-source":
				m := f.store.vlessRuntimeMaterials[f.materialID]
				m.NativeConfig = json.RawMessage(`{broken`)
				f.store.vlessRuntimeMaterials[m.ID] = m
			case "ambiguous-source":
				peer := member
				peer.NodeID = "native-peer"
				f.store.nodes[peer.NodeID] = nodes.Node{ID: peer.NodeID, Capabilities: f.store.nodes[f.nodeID].Capabilities}
				f.store.membersByGroup[peer.GroupID][peer.NodeID] = peer
				f.store.syncGroupMemberToEndpointPoolsLocked(peer)
				m := cloneVLESSRuntimeMaterial(f.store.vlessRuntimeMaterials[f.materialID])
				m.ID, m.NodeID = "different-material", peer.NodeID
				var config map[string]any
				if err := json.Unmarshal(m.NativeConfig, &config); err != nil {
					t.Fatal(err)
				}
				config["outbounds"].([]any)[0].(map[string]any)["settings"] = map[string]any{"domainStrategy": "UseIPv4"}
				m.NativeConfig, _ = json.Marshal(config)
				f.store.vlessRuntimeMaterials[m.ID] = m
			case "compile-failure":
				f.store.revisionsByGroup[member.GroupID] = []generations.GroupRevision{{ID: "bad", GroupID: member.GroupID, Revision: 1, Engine: agentv1.EngineGOST, Config: json.RawMessage(`{broken`)}}
			}
			count, audits := len(f.store.vlessRuntimeMaterials), len(f.store.auditEvents)
			_, _, err := f.store.UpsertGroupMember(context.Background(), member, projectionAudit("join", member.UpdatedAt))
			if failure == "revoked" {
				if err != nil || len(f.store.vlessRuntimeMaterials) != count {
					t.Fatal("explicit revocation was replaced", err)
				}
				if _, err := f.store.RuntimeMaterialForNode(context.Background(), member.NodeID, f.bindingID); err == nil {
					t.Fatal("revoked member received a runtime secret")
				}
				return
			}
			if err == nil || len(f.store.vlessRuntimeMaterials) != count || len(f.store.auditEvents) != audits {
				t.Fatal("failed provisioning leaked material or audit state", err)
			}
			if _, exists := f.store.membersByGroup[member.GroupID][member.NodeID]; exists {
				t.Fatal("failed provisioning retained membership")
			}
			if _, exists := f.store.endpointMembers[f.endpointID][member.NodeID]; exists {
				t.Fatal("failed provisioning retained endpoint candidate")
			}
		})
	}
}

func TestHeartbeatReconcilesSpeedLimitCapabilityChanges(t *testing.T) {
	f := automaticMemberFixture(t)
	f.rule.SpeedLimitMbps = 20
	f.store.forwardRules[f.rule.ID] = f.rule
	base := append([]string(nil), f.store.nodes[f.nodeID].Capabilities...)
	for _, supported := range []bool{true, false} {
		caps := append([]string(nil), base...)
		if supported {
			caps = append(caps, agentconfig.CapabilityLinuxTCSpeedLimit)
		}
		if _, err := f.store.UpdateHeartbeat(context.Background(), f.nodeID, nodes.Heartbeat{Capabilities: caps}, f.rule.CreatedAt.Add(time.Minute), projectionAudit("tc", f.rule.CreatedAt)); err != nil {
			t.Fatal(err)
		}
		config, _, err := f.store.DesiredNodeConfig(context.Background(), f.nodeID)
		if err != nil {
			t.Fatal(err)
		}
		var bundle agentv1.ConfigurationBundle
		if err := json.Unmarshal(config.Config, &bundle); err != nil {
			t.Fatal(err)
		}
		if (len(bundle.Fragments) == 1) != supported {
			t.Fatal("limited rule did not follow TC capability")
		}
	}
}
