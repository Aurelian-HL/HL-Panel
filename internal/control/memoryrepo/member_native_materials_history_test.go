package memoryrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
	"github.com/hongle/hl-panel/internal/securetoken"
)

func registerNativeReplacement(ctx context.Context, f vlessBundleFixture, service *enrollment.Service, input enrollment.EnrollInput, nodeID, mode string) (string, error) {
	if mode == "enroll" {
		identity, err := service.Enroll(ctx, input)
		return identity.NodeID, err
	}
	node, err := service.Reenroll(ctx, f.store.nodes[nodeID], input)
	return node.ID, err
}

func alterNativeOutbound(t *testing.T, material vlessruntime.Material) vlessruntime.Material {
	t.Helper()
	material = cloneVLESSRuntimeMaterial(material)
	var config map[string]any
	if err := json.Unmarshal(material.NativeConfig, &config); err != nil {
		t.Fatal(err)
	}
	config["outbounds"].([]any)[0].(map[string]any)["settings"] = map[string]any{"domainStrategy": "UseIPv4"}
	var err error
	material.NativeConfig, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	return material
}

func TestNativeMemberHistoricalFallbackRejectsInvalidAndAmbiguousSources(t *testing.T) {
	for _, failure := range []string{"invalid-source", "ambiguous-source"} {
		for _, registration := range []string{"enroll", "reenroll"} {
			t.Run(failure+"/"+registration, func(t *testing.T) {
				f, _, input, nodeID := nativeReenrollmentFixture(t)
				ctx, at := context.Background(), f.rule.CreatedAt.Add(2*time.Minute)
				sourceID := f.materialID
				if failure == "ambiguous-source" {
					member := nativeJoiningMember(f)
					peer := cloneNode(f.store.nodes[f.nodeID])
					peer.ID, peer.CredentialHash = member.NodeID, securetoken.Hash("historical-peer")
					f.store.nodes[peer.ID], f.store.nodesByCredential[peer.CredentialHash] = peer, peer.ID
					if _, _, err := f.store.UpsertGroupMember(ctx, member, projectionAudit("historical-peer", at)); err != nil {
						t.Fatal(err)
					}
					material, err := f.store.RuntimeMaterialForNode(ctx, peer.ID, f.bindingID)
					if err != nil {
						t.Fatal(err)
					}
					sourceID = material.ID
					if _, err := f.store.RetireGroupMember(ctx, member.GroupID, peer.ID, at, projectionAudit("retire-peer", at)); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := f.store.RetireGroupMember(ctx, f.rule.EntryGroupID, f.nodeID, at, projectionAudit("retire-source", at)); err != nil {
					t.Fatal(err)
				}
				before, err := f.store.EncodeSnapshot()
				if err != nil {
					t.Fatal(err)
				}
				original := cloneVLESSRuntimeMaterial(f.store.vlessRuntimeMaterials[sourceID])
				broken := cloneVLESSRuntimeMaterial(original)
				if failure == "invalid-source" {
					broken.NativeConfig = json.RawMessage(`{broken`)
				} else {
					broken = alterNativeOutbound(t, original)
					if err := f.store.validatePersistedVLESSRuntimeMaterialLocked(broken, nil); err != nil {
						t.Fatal("ambiguity fixture is invalid", err)
					}
				}
				f.store.vlessRuntimeMaterials[sourceID] = broken
				service := enrollment.NewService(f.store, func() time.Time { return at.Add(time.Minute) }, time.Hour)
				if _, err := registerNativeReplacement(ctx, f, service, input, nodeID, registration); !errors.Is(err, faults.ErrConflict) {
					t.Fatal("invalid retained import did not reject registration", err)
				}
				f.store.vlessRuntimeMaterials[sourceID] = original
				after, err := f.store.EncodeSnapshot()
				if err != nil {
					t.Fatal(err)
				}
				assertUDPSnapshotUnchanged(t, before, after)
				if t.Failed() {
					return
				}
				id, err := registerNativeReplacement(ctx, f, service, input, nodeID, registration)
				if err != nil {
					t.Fatal("failed historical provisioning consumed the token", err)
				}
				requireAutomaticRuleBundle(t, f, id)
			})
		}
	}
}

func TestNativeMemberHistoricalFallbackNeverRevivesRevokedSource(t *testing.T) {
	for _, registration := range []string{"enroll", "reenroll"} {
		t.Run(registration, func(t *testing.T) {
			f, _, input, nodeID := nativeReenrollmentFixture(t)
			ctx, at := context.Background(), f.store.vlessRuntimeMaterials[f.materialID].CreatedAt.Add(time.Minute)
			runtime, err := vlessruntime.NewService(f.store, func() time.Time { return at })
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := runtime.Revoke(ctx, "admin", f.materialID, 1, "revoke-source"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.RetireGroupMember(ctx, f.rule.EntryGroupID, f.nodeID, at, projectionAudit("retire-revoked-source", at)); err != nil {
				t.Fatal(err)
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
			token, err := service.IssueForGroup(ctx, "admin", "replacement-after-revocation", f.rule.EntryGroupID, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			input.RawToken = token.Token
			id, err := registerNativeReplacement(ctx, f, service, input, nodeID, registration)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.RuntimeMaterialForNode(ctx, id, f.bindingID); !errors.Is(err, faults.ErrNotFound) {
				t.Fatal("revoked history granted new runtime credentials", err)
			}
			if len(f.store.vlessRuntimeMaterials) != 1 || f.store.vlessRuntimeMaterials[f.materialID].State != vlessruntime.StateRevoked {
				t.Fatal("registration revived or copied revoked history")
			}
		})
	}
}

func TestNativeMemberProvisioningPrefersLiveSourceOverRetainedHistory(t *testing.T) {
	f, service, input, nodeID := nativeReenrollmentFixture(t)
	ctx, at := context.Background(), f.rule.CreatedAt.Add(2*time.Minute)
	member := nativeJoiningMember(f)
	peer := cloneNode(f.store.nodes[f.nodeID])
	peer.ID, peer.CredentialHash = member.NodeID, securetoken.Hash("live-native-peer")
	f.store.nodes[peer.ID], f.store.nodesByCredential[peer.CredentialHash] = peer, peer.ID
	if _, _, err := f.store.UpsertGroupMember(ctx, member, projectionAudit("live-peer", at)); err != nil {
		t.Fatal(err)
	}
	current, err := f.store.RuntimeMaterialForNode(ctx, peer.ID, f.bindingID)
	if err != nil {
		t.Fatal(err)
	}
	current = alterNativeOutbound(t, current)
	f.store.vlessRuntimeMaterials[current.ID] = current
	if _, err := f.store.RetireGroupMember(ctx, f.rule.EntryGroupID, f.nodeID, at, projectionAudit("retire-old-source", at)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Reenroll(ctx, f.store.nodes[nodeID], input); err != nil {
		t.Fatal("retained history overrode the current source", err)
	}
	material, err := f.store.RuntimeMaterialForNode(ctx, nodeID, f.bindingID)
	if err != nil || !bytes.Equal(material.NativeConfig, current.NativeConfig) {
		t.Fatal("new member received stale historical material", err)
	}
	if _, err := f.store.RuntimeMaterialForNode(ctx, f.nodeID, f.bindingID); !errors.Is(err, faults.ErrNotFound) {
		t.Fatal("retired source was reauthorized", err)
	}
}
