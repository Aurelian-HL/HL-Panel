package memoryrepo

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
	"github.com/hongle/hl-panel/internal/securetoken"
)

func TestNativeCredentialsRotationPreservesIdentityAndIsAtomic(t *testing.T) {
	f, p := nativeRuntimeFixture(t)
	var config map[string]any
	json.Unmarshal(p.Config, &config)
	config["outbounds"].([]any)[0] = map[string]any{"tag": "direct", "protocol": "socks", "settings": map[string]any{"servers": []any{map[string]any{"address": "proxy.example.org", "port": 1080, "users": []any{map[string]any{"user": "old-user", "pass": "old-secret"}}}}}}
	p.Config, _ = json.Marshal(config)
	svc, _ := vlessruntime.NewService(f.store, time.Now)
	m, _, err := svc.BindImported(context.Background(), "admin", f.bindingID, f.nodeID, p.Config, "credential-fixture")
	if err != nil {
		t.Fatal(err)
	}
	request := requestFromNative(f.rule)
	request.Paused = false
	f.rule, _, err = forwarding.NewService(f.store, time.Now).Update(context.Background(), "admin", f.rule.ID, request, "activate-fixture")
	if err != nil {
		t.Fatal(err)
	}
	runtimeRevision := f.rule.EffectiveRuntimeRevision()
	generation := f.store.nodes[f.nodeID].DesiredGeneration
	request = requestFromNative(f.rule)
	request.Name = "renamed native listener"
	f.rule, _, err = forwarding.NewService(f.store, time.Now).Update(context.Background(), "admin", f.rule.ID, request, "rename-before-rotation")
	if err != nil || f.rule.EffectiveRuntimeRevision() != runtimeRevision || f.store.nodes[f.nodeID].DesiredGeneration != generation {
		t.Fatal("native display edit deployed runtime", err)
	}
	in := vlessruntime.NativeCredentialsRotation{AdministratorID: "admin", RuleID: f.rule.ID, ExpectedRevision: f.rule.Revision, MaterialRevisions: map[string]int64{m.ID: 1}, Host: "proxy.example.org", Port: 1080, Username: "new-user", Password: "new-secret", IdempotencyKey: "test-native-rotation"}
	before, _ := f.store.EncodeSnapshot()
	bad := in
	bad.AdministratorID = "unknown"
	if _, err = f.store.RotateNativeSOCKS5Credentials(context.Background(), bad); !errors.Is(err, faults.ErrNotFound) {
		t.Fatal("authorization", err)
	}
	bad = in
	bad.Host = "different.example.org"
	if _, err = f.store.RotateNativeSOCKS5Credentials(context.Background(), bad); !errors.Is(err, faults.ErrConflict) {
		t.Fatal("target match", err)
	}
	bad = in
	bad.ExpectedRevision++
	if _, err = f.store.RotateNativeSOCKS5Credentials(context.Background(), bad); !errors.Is(err, faults.ErrConflict) {
		t.Fatal("CAS", err)
	}
	afterFailures, _ := f.store.EncodeSnapshot()
	if string(before) != string(afterFailures) {
		t.Fatal("failed operation mutated state")
	}
	result, err := f.store.RotateNativeSOCKS5Credentials(context.Background(), in)
	if err != nil || result.Materials != 1 || result.Revision != in.ExpectedRevision+1 {
		t.Fatal(result, err)
	}
	if rule := f.store.forwardRules[f.rule.ID]; rule.EffectiveRuntimeRevision() != result.Revision || rule.EffectiveRuntimeRevision() <= runtimeRevision {
		t.Fatal("credential rotation reused management-only runtime identity")
	}
	material, err := f.store.RuntimeMaterialForNode(context.Background(), f.nodeID, f.bindingID)
	if err != nil {
		t.Fatal(err)
	}
	var updated map[string]any
	json.Unmarshal(material.NativeConfig, &updated)
	if !reflect.DeepEqual(config["inbounds"], updated["inbounds"]) || !reflect.DeepEqual(config["routing"], updated["routing"]) || material.CredentialUUID != p.UUID || material.RealityPrivateKey != p.PrivateKey {
		t.Fatal("identity/route changed")
	}
	user := updated["outbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any)["servers"].([]any)[0].(map[string]any)["users"].([]any)[0].(map[string]any)
	if user["pass"] != "new-secret" || user["user"] != "new-user" {
		t.Fatal("credentials not changed")
	}
	if again, err := f.store.RotateNativeSOCKS5Credentials(context.Background(), in); err != nil || !again.Replayed {
		t.Fatal("retry", again, err)
	}
	bad = in
	bad.Password = "other-secret"
	if _, err = f.store.RotateNativeSOCKS5Credentials(context.Background(), bad); !errors.Is(err, faults.ErrIdempotencyConflict) {
		t.Fatal("conflicting retry", err)
	}
	raw, err := f.store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeSnapshot(raw); err != nil {
		t.Fatal("restore", err)
	}
	fragments, err := f.store.compileForwardingFragmentsLocked(f.nodeID)
	if err != nil || len(fragments) != 1 || !strings.Contains(string(fragments[0].Config), "new-secret") {
		t.Fatal("updated bundle", err)
	}
	for _, event := range f.store.AuditEvents() {
		b, _ := json.Marshal(event)
		if stringContainsAny(string(b), "new-secret", "old-secret", "new-user") {
			t.Fatal("audit leaked credentials")
		}
	}
}

func stringContainsAny(s string, values ...string) bool {
	for _, v := range values {
		if strings.Contains(s, v) {
			return true
		}
	}
	return false
}

func TestNativeCredentialsRejectsAmbiguousServer(t *testing.T) {
	raw := json.RawMessage(`{"outbounds":[{"protocol":"socks","settings":{"servers":[{"address":"proxy","port":1080,"users":[{"user":"x","pass":"y"}]},{"address":"proxy","port":1080,"users":[{"user":"x","pass":"y"}]}]}}]}`)
	if _, err := rotateNativeSOCKS5JSON(raw, "proxy", 1080, "new", "secret"); !errors.Is(err, faults.ErrConflict) {
		t.Fatal(err)
	}
}

func TestNativeCredentialsRotationIncludesRetiredMemberWithoutReactivatingIt(t *testing.T) {
	f, profile := nativeRuntimeFixture(t)
	ctx := context.Background()
	var config map[string]any
	if err := json.Unmarshal(profile.Config, &config); err != nil {
		t.Fatal(err)
	}
	config["outbounds"].([]any)[0] = map[string]any{"tag": "direct", "protocol": "socks", "settings": map[string]any{"servers": []any{map[string]any{"address": "proxy.example.org", "port": 1080, "users": []any{map[string]any{"user": "old-user", "pass": "old-secret"}}}}}}
	profile.Config, _ = json.Marshal(config)
	service, _ := vlessruntime.NewService(f.store, time.Now)
	bound, _, err := service.BindImported(ctx, "admin", f.bindingID, f.nodeID, profile.Config, "rotation-retired-fixture")
	if err != nil {
		t.Fatal(err)
	}
	material := f.store.vlessRuntimeMaterials[bound.ID]
	f.materialID = material.ID
	request := requestFromNative(f.rule)
	request.Paused = false
	f.rule, _, err = forwarding.NewService(f.store, time.Now).Update(ctx, "admin", f.rule.ID, request, "rotation-retired-activate")
	if err != nil {
		t.Fatal(err)
	}
	member := nativeJoiningMember(f)
	node := cloneNode(f.store.nodes[f.nodeID])
	node.ID, node.CredentialHash = member.NodeID, securetoken.Hash("rotation-retired-peer")
	f.store.nodes[node.ID] = node
	f.store.nodesByCredential[node.CredentialHash] = node.ID
	if _, _, err := f.store.UpsertGroupMember(ctx, member, projectionAudit("join-rotation-peer", member.UpdatedAt)); err != nil {
		t.Fatal(err)
	}
	peer, err := f.store.RuntimeMaterialForNode(ctx, member.NodeID, f.bindingID)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(time.Minute)
	if _, err := f.store.RetireGroupMember(ctx, member.GroupID, f.nodeID, at, projectionAudit("retire-before-rotation", at)); err != nil {
		t.Fatal(err)
	}
	retiredConfig, _, err := f.store.DesiredNodeConfig(ctx, f.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	binding := f.store.vlessBindings[f.bindingID]
	in := vlessruntime.NativeCredentialsRotation{AdministratorID: "admin", RuleID: f.rule.ID, ExpectedRevision: f.rule.Revision, MaterialRevisions: map[string]int64{material.ID: material.Revision, peer.ID: peer.Revision}, Host: "proxy.example.org", Port: 1080, Username: "new-user", Password: "new-secret", IdempotencyKey: "rotation-with-retired-member"}
	result, err := f.store.RotateNativeSOCKS5Credentials(ctx, in)
	if err != nil || result.Materials != 2 {
		t.Fatal("retired history prevented credential rotation", result, err)
	}
	requireRetiredMaterialUnavailable(t, f.store, f)
	after, _, err := f.store.DesiredNodeConfig(ctx, f.nodeID)
	if err != nil || !reflect.DeepEqual(retiredConfig, after) {
		t.Fatal("credential rotation changed the retired node configuration", err)
	}
	if !reflect.DeepEqual(binding, f.store.vlessBindings[f.bindingID]) {
		t.Fatal("credential rotation changed the subscription identity")
	}
	for _, original := range []vlessruntime.Material{material, peer} {
		updated := f.store.vlessRuntimeMaterials[original.ID]
		if updated.CredentialUUID != original.CredentialUUID || updated.RealityPrivateKey != original.RealityPrivateKey || updated.Revision != original.Revision+1 {
			t.Fatal("credential rotation changed the ingress identity or lost a revision")
		}
		var updatedConfig map[string]any
		if err := json.Unmarshal(updated.NativeConfig, &updatedConfig); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(config["inbounds"], updatedConfig["inbounds"]) || !reflect.DeepEqual(config["routing"], updatedConfig["routing"]) {
			t.Fatal("credential rotation changed ingress or routing")
		}
		if !strings.Contains(string(updated.NativeConfig), "new-secret") || strings.Contains(string(updated.NativeConfig), "old-secret") {
			t.Fatal("a member retained stale credentials")
		}
	}
	raw, err := f.store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal("rotation poisoned the persisted snapshot", err)
	}
	requireRetiredMaterialUnavailable(t, restored, f)
	fragments, err := restored.compileForwardingFragmentsLocked(member.NodeID)
	if err != nil || len(fragments) != 1 || !strings.Contains(string(fragments[0].Config), "new-secret") {
		t.Fatal("serving member did not receive the new credentials", err)
	}
	rejoin := restored.membersByGroup[member.GroupID][f.nodeID]
	rejoin.RetiredAt, rejoin.UpdatedAt = nil, at.Add(time.Second)
	if _, _, err := restored.UpsertGroupMember(ctx, rejoin, projectionAudit("rejoin-after-rotation", rejoin.UpdatedAt)); err != nil {
		t.Fatal(err)
	}
	rejoined, err := restored.RuntimeMaterialForNode(ctx, f.nodeID, f.bindingID)
	if err != nil || rejoined.CredentialUUID != material.CredentialUUID || !strings.Contains(string(rejoined.NativeConfig), "new-secret") {
		t.Fatal("rejoining restored stale credentials or changed identity", err)
	}
}
