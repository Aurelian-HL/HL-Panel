package memoryrepo

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func desiredVLESSBundle(t *testing.T, fixture vlessBundleFixture) (int64, string, agentv1.ConfigurationBundle) {
	t.Helper()
	generation := fixture.store.nodes[fixture.nodeID].DesiredGeneration
	if generation == 0 {
		t.Fatal("node has no desired configuration")
	}
	configuration, exists := fixture.store.nodeConfigsByNode[fixture.nodeID][generation]
	if !exists {
		t.Fatalf("node desired generation %d was not stored", generation)
	}
	var bundle agentv1.ConfigurationBundle
	if err := json.Unmarshal(configuration.Config, &bundle); err != nil {
		t.Fatal(err)
	}
	return generation, string(configuration.Config), bundle
}

func vlessMutationEvent(t *testing.T, at time.Time, action, resourceType, resourceID string) audit.Event {
	t.Helper()
	event, err := audit.NewEvent(at, "administrator", "admin", action, resourceType, resourceID, "succeeded", nil)
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func TestVLESSIdentityRotateRemovesOldDesiredSecretsAndNewMaterialActivates(t *testing.T) {
	fixture := newVLESSBundleFixture(t, true)
	initialAt := fixture.store.vlessBindings[fixture.bindingID].Binding.UpdatedAt
	if _, _, err := fixture.store.compileNodeConfigLocked(fixture.nodeID, initialAt); err != nil {
		t.Fatal(err)
	}
	initialGeneration, initialRaw, initialBundle := desiredVLESSBundle(t, fixture)
	if len(initialBundle.Fragments) != 1 || !strings.Contains(initialRaw, fixture.credential) || !strings.Contains(initialRaw, fixture.privateKey) {
		t.Fatal("initial desired bundle lacks the active VLESS runtime material")
	}

	rotatedUUID := "4d506676-e698-44f0-b7ae-bc56ae48f394"
	rotateAt := initialAt.Add(time.Minute)
	rotate := vlessidentity.RotateInput{BindingID: fixture.bindingID, CredentialUUID: rotatedUUID,
		ExpectedRevision: 1, IdempotencyKey: "rotate-bundle", RequestSHA256: strings.Repeat("a", 64),
		UpdatedBy: "admin", UpdatedAt: rotateAt}
	if _, replayed, err := fixture.store.Rotate(context.Background(), rotate,
		vlessMutationEvent(t, rotateAt, "vless_identity.rotate", "vless_identity_binding", fixture.bindingID)); err != nil || replayed {
		t.Fatalf("rotate identity: replay=%v err=%v", replayed, err)
	}
	rotatedGeneration, rotatedRaw, rotatedBundle := desiredVLESSBundle(t, fixture)
	if rotatedGeneration != initialGeneration+1 || len(rotatedBundle.Fragments) != 0 ||
		strings.Contains(rotatedRaw, fixture.credential) || strings.Contains(rotatedRaw, fixture.privateKey) {
		t.Fatalf("rotation did not withdraw old VLESS listener: generation=%d bundle=%s", rotatedGeneration, rotatedRaw)
	}
	if material := fixture.store.vlessRuntimeMaterials[fixture.materialID]; material.State != vlessruntime.StateRevoked || material.CredentialUUID != "" || material.RealityPrivateKey != "" {
		t.Fatalf("old runtime material remains active or retains secrets: %#v", material.Public())
	}
	if _, replayed, err := fixture.store.Rotate(context.Background(), rotate,
		vlessMutationEvent(t, rotateAt, "vless_identity.rotate", "vless_identity_binding", fixture.bindingID)); err != nil || !replayed {
		t.Fatalf("rotate replay: replay=%v err=%v", replayed, err)
	}
	if current := fixture.store.nodes[fixture.nodeID].DesiredGeneration; current != rotatedGeneration {
		t.Fatalf("idempotent replay advanced desired generation to %d", current)
	}

	newMaterialID := "material-vless-rotated"
	bindAt := rotateAt.Add(time.Minute)
	if _, replayed, err := fixture.store.SaveRuntimeMaterial(context.Background(), vlessruntime.SaveInput{
		ID: newMaterialID, BindingID: fixture.bindingID, NodeID: fixture.nodeID,
		RealityPrivateKey: fixture.privateKey, ExpectedRevision: 0,
		IdempotencyKey: "bind-rotated", RequestSHA256: strings.Repeat("b", 64),
		CreatedBy: "admin", CreatedAt: bindAt,
	}, vlessMutationEvent(t, bindAt, "vless_runtime.bind", "vless_runtime_material", newMaterialID)); err != nil || replayed {
		t.Fatalf("bind rotated material: replay=%v err=%v", replayed, err)
	}
	activeGeneration, activeRaw, activeBundle := desiredVLESSBundle(t, fixture)
	if activeGeneration != rotatedGeneration+1 || len(activeBundle.Fragments) != 1 ||
		!strings.Contains(activeRaw, rotatedUUID) || !strings.Contains(activeRaw, fixture.privateKey) ||
		strings.Contains(activeRaw, fixture.credential) {
		t.Fatalf("new runtime material was not activated cleanly: generation=%d bundle=%s", activeGeneration, activeRaw)
	}
}

func TestVLESSRuntimeRevokeAndIdentityRevokeWithdrawDesiredListener(t *testing.T) {
	for _, revokeIdentity := range []bool{false, true} {
		name := "runtime"
		if revokeIdentity {
			name = "identity"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newVLESSBundleFixture(t, true)
			initialAt := fixture.store.vlessBindings[fixture.bindingID].Binding.UpdatedAt
			if _, _, err := fixture.store.compileNodeConfigLocked(fixture.nodeID, initialAt); err != nil {
				t.Fatal(err)
			}
			initialGeneration, _, _ := desiredVLESSBundle(t, fixture)
			revokeAt := initialAt.Add(time.Minute)
			if revokeIdentity {
				_, replayed, err := fixture.store.Revoke(context.Background(), vlessidentity.RevokeInput{
					BindingID: fixture.bindingID, ExpectedRevision: 1,
					IdempotencyKey: "revoke-identity-bundle", RequestSHA256: strings.Repeat("c", 64),
					UpdatedBy: "admin", UpdatedAt: revokeAt,
				}, vlessMutationEvent(t, revokeAt, "vless_identity.revoke", "vless_identity_binding", fixture.bindingID))
				if err != nil || replayed {
					t.Fatalf("revoke identity: replay=%v err=%v", replayed, err)
				}
			} else {
				_, replayed, err := fixture.store.RevokeRuntimeMaterial(context.Background(), vlessruntime.RevokeInput{
					ID: fixture.materialID, ExpectedRevision: 1,
					IdempotencyKey: "revoke-runtime-bundle", RequestSHA256: strings.Repeat("d", 64),
					UpdatedBy: "admin", UpdatedAt: revokeAt,
				}, vlessMutationEvent(t, revokeAt, "vless_runtime.revoke", "vless_runtime_material", fixture.materialID))
				if err != nil || replayed {
					t.Fatalf("revoke runtime material: replay=%v err=%v", replayed, err)
				}
			}
			generation, raw, bundle := desiredVLESSBundle(t, fixture)
			if generation != initialGeneration+1 || len(bundle.Fragments) != 0 ||
				strings.Contains(raw, fixture.credential) || strings.Contains(raw, fixture.privateKey) {
				t.Fatalf("revocation did not withdraw desired listener: generation=%d bundle=%s", generation, raw)
			}
		})
	}
}

func TestActivatedVLESSRuleSurvivesUnrelatedNodeBundleRecompile(t *testing.T) {
	fixture := newVLESSBundleFixture(t, true)
	now := fixture.store.vlessBindings[fixture.bindingID].Binding.UpdatedAt
	if _, _, err := fixture.store.compileNodeConfigLocked(fixture.nodeID, now); err != nil {
		t.Fatal(err)
	}
	rule := fixture.store.forwardRules[fixture.rule.ID]
	rule.Deployed = true
	rule.Status = forwarding.StatusActive
	fixture.store.forwardRules[rule.ID] = rule

	// A later compile is representative of a group membership, probe, or node
	// metadata change. The active listener must remain in the new bundle.
	compiled, _, err := fixture.store.compileNodeConfigLocked(fixture.nodeID, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	var bundle agentv1.ConfigurationBundle
	if err := json.Unmarshal(compiled.Config, &bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Fragments) != 1 || bundle.Fragments[0].Engine != agentv1.EngineXray {
		t.Fatalf("activated VLESS listener was dropped during recompile: %#v", bundle.Fragments)
	}
}
