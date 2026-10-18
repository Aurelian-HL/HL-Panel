package memoryrepo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/importedconfig"
	"github.com/hongle/hl-panel/internal/control/importedconfig/testfixture"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
)

func nativeRuntimeFixture(t *testing.T) (vlessBundleFixture, importedconfig.NativeProfile) {
	t.Helper()
	f := newVLESSBundleFixture(t, true)
	a := testfixture.Document()
	a["inbounds"].([]any)[0].(map[string]any)["port"] = f.rule.ListenPort
	a["inbounds"].([]any)[0].(map[string]any)["raw"].(map[string]any)["port"] = f.rule.ListenPort
	a["config"].(map[string]any)["inbounds"].([]any)[0].(map[string]any)["port"] = f.rule.ListenPort
	raw, _ := json.Marshal(a)
	p, err := importedconfig.PrepareNative(raw, 1)
	if err != nil {
		t.Fatal(err)
	}
	r := f.store.forwardRules[f.rule.ID]
	r.Paused = true
	r.VLESSOutboundMode = forwarding.VLESSOutboundNative
	r.VLESSSOCKS5Host = ""
	r.VLESSSOCKS5Port = 0
	r.VLESSSOCKS5Username = ""
	r.Targets = []forwarding.Target{}
	r.RealityServerName = p.ServerNames[0]
	r.RealityShortID = p.ShortIDs[0]
	r.RealityDestination = p.Destination
	r.VLESSFlow = p.Flow
	r.VLESSFlowExplicit = true
	r.RealityPublicKey, _ = vlessruntime.RealityPublicKey(p.PrivateKey)
	f.store.forwardRules[r.ID] = r
	delete(f.store.vlessSOCKS5Upstreams, r.ID)
	delete(f.store.vlessRuntimeMaterials, f.materialID)
	b := f.store.vlessBindings[f.bindingID]
	b.CredentialUUID = p.UUID
	b.Binding.ImportedFrom = &vlessidentity.ImportOrigin{BatchID: "synthetic-batch", LineID: 1}
	f.store.vlessBindings[f.bindingID] = b
	pool := f.store.endpointPools[f.endpointID]
	pool.Port = r.ListenPort
	f.store.endpointPools[f.endpointID] = pool
	f.rule = r
	return f, p
}

func TestNativeRuntimePersistsAndPauseResumeCompilesExactMaterial(t *testing.T) {
	f, p := nativeRuntimeFixture(t)
	ctx := context.Background()
	service, _ := vlessruntime.NewService(f.store, time.Now)
	material, replay, err := service.BindImported(ctx, "admin", f.bindingID, f.nodeID, p.Config, "native-bind")
	if err != nil || replay {
		t.Fatal("import bind failed", err)
	}
	if _, replay, err = service.BindImported(ctx, "admin", f.bindingID, f.nodeID, p.Config, "native-bind"); err != nil || !replay {
		t.Fatal("import replay failed", err)
	}
	public, _ := json.Marshal(material)
	if strings.Contains(string(public), p.UUID) || strings.Contains(string(public), p.PrivateKey) || strings.Contains(string(public), "mldsa65Seed") {
		t.Fatal("public secret leak")
	}
	if fragments, err := f.store.compileForwardingFragmentsLocked(f.nodeID); err != nil || len(fragments) != 0 {
		t.Fatal("paused import published listener", err)
	}
	raw, err := f.store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal("native snapshot failed", err)
	}
	stored, err := restored.RuntimeMaterialForNode(ctx, f.nodeID, f.bindingID)
	if err != nil || string(stored.NativeConfig) != string(p.Config) {
		t.Fatal("native material lost on restart", err)
	}
	stored.NativeConfig[0] = '!'
	original, err := restored.RuntimeMaterialForNode(ctx, f.nodeID, f.bindingID)
	if err != nil || original.NativeConfig[0] == '!' {
		t.Fatal("native material aliased")
	}
	// Operational pause changes traverse the normal update repository/service.
	r := restored.forwardRules[f.rule.ID]
	request := requestFromNative(r)
	request.Paused = false
	updated, _, err := forwarding.NewService(restored, time.Now).Update(ctx, "admin", r.ID, request, "native-resume")
	if err != nil {
		t.Fatal("resume failed", err)
	}
	fragments, err := restored.compileForwardingFragmentsLocked(f.nodeID)
	if err != nil || len(fragments) != 1 || !strings.Contains(string(fragments[0].Config), "mldsa65Seed") || !strings.Contains(string(fragments[0].Config), p.UUID) {
		t.Fatal("resumed import lost native semantics", err)
	}
	request = requestFromNative(updated)
	request.RealityShortID = "ff"
	if _, _, err = forwarding.NewService(restored, time.Now).Update(ctx, "admin", r.ID, request, "native-corrupt"); err == nil {
		t.Fatal("native transport edited without matching private material")
	}
	request = requestFromNative(updated)
	request.Paused = true
	if _, _, err = forwarding.NewService(restored, time.Now).Update(ctx, "admin", r.ID, request, "native-pause"); err != nil {
		t.Fatal(err)
	}
	fragments, err = restored.compileForwardingFragmentsLocked(f.nodeID)
	if err != nil || len(fragments) != 0 {
		t.Fatal("paused native listener survived", err)
	}
	if _, _, err = service.Revoke(ctx, "admin", material.ID, 1, "native-revoke"); err != nil {
		t.Fatal(err)
	}
	raw, err = f.store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeSnapshot(raw); err != nil {
		t.Fatal("revoked native history failed restore", err)
	}
}

func TestNativeUDPRuntimePassesTargetProtocolWithoutChangingRealityIngress(t *testing.T) {
	f, p := nativeRuntimeFixture(t)
	ctx := context.Background()
	service, _ := vlessruntime.NewService(f.store, time.Now)
	if _, replay, err := service.BindImported(ctx, "admin", f.bindingID, f.nodeID, p.Config, "native-udp-bind"); err != nil || replay {
		t.Fatalf("import bind failed: replay=%v err=%v", replay, err)
	}
	rule := f.store.forwardRules[f.rule.ID]
	// Native transport parameters are immutable through the ordinary update
	// service. This test models the persisted UDP rule produced by the import
	// workflow, then exercises the same bundle compiler path.
	rule.Protocol = forwarding.ProtocolUDP
	rule.Paused = false
	f.store.forwardRules[rule.ID] = rule
	fragments, err := f.store.compileForwardingFragmentsLocked(f.nodeID)
	if err != nil || len(fragments) != 1 {
		t.Fatalf("native UDP did not compile: fragments=%d err=%v", len(fragments), err)
	}
	var config map[string]any
	if err := json.Unmarshal(fragments[0].Config, &config); err != nil {
		t.Fatal(err)
	}
	in := config["inbounds"].([]any)[0].(map[string]any)
	stream := in["streamSettings"].(map[string]any)
	if in["protocol"] != "vless" || stream["network"] != "tcp" || stream["security"] != "reality" {
		t.Fatalf("native UDP changed ingress transport: %#v", in)
	}
	tag := in["tag"].(string)
	metadata, err := base64.RawURLEncoding.DecodeString(strings.SplitN(tag, "--", 2)[1])
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]string
	if err := json.Unmarshal(metadata, &meta); err != nil || meta["protocol"] != "udp" {
		t.Fatalf("native UDP metadata = %#v", meta)
	}
}

func requestFromNative(r forwarding.Rule) forwarding.Request {
	return forwarding.Request{Name: r.Name, CustomerID: r.CustomerID, OwnerKind: r.OwnerKind, OwnerID: r.OwnerID, RuleGroupID: r.RuleGroupID, EntryGroupID: r.EntryGroupID, EgressMode: r.EgressMode, VLESSOutboundMode: r.VLESSOutboundMode, IngressProtocol: r.IngressProtocol, VLESSFlow: r.VLESSFlow, VLESSFlowExplicit: r.VLESSFlowExplicit, RealityServerName: r.RealityServerName, RealityPublicKey: r.RealityPublicKey, RealityShortID: r.RealityShortID, RealityDestination: r.RealityDestination, Protocol: r.Protocol, ListenPort: r.ListenPort, Targets: r.Targets, SelectionPolicy: r.SelectionPolicy, Paused: r.Paused, Description: r.Description, Revision: r.Revision}
}

func TestNativeOrdinaryTransferCannotCreateOrOverwrite(t *testing.T) {
	f, _ := nativeRuntimeFixture(t)
	for _, overwrite := range []bool{false, true} {
		candidate := forwarding.ImportCandidate{Line: 1, Operation: forwarding.ImportUpsert, Rule: f.rule, ExpectedRevision: f.rule.Revision}
		if overwrite {
			candidate.Rule.VLESSOutboundMode = forwarding.VLESSOutboundDirect
		} else {
			candidate.Rule.ID = "forged-native"
		}
		evaluations, working := f.store.evaluateForwardingImportLocked([]forwarding.ImportCandidate{candidate})
		if len(evaluations) != 1 || evaluations[0].Issue == nil || working[f.rule.ID].VLESSOutboundMode != forwarding.VLESSOutboundNative {
			t.Fatal("ordinary transfer bypassed native protection")
		}
		if _, exists := working["forged-native"]; exists {
			t.Fatal("ordinary transfer created native listener")
		}
	}
}

func TestNativeRuleRequiresCurrentSnapshotVersionEvenWithoutMaterial(t *testing.T) {
	f, _ := nativeRuntimeFixture(t)
	raw, err := f.store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		t.Fatal("invalid snapshot")
	}
	doc["Version"] = json.RawMessage(`19`)
	raw, _ = json.Marshal(doc)
	if _, err := DecodeSnapshot(raw); err == nil || !strings.Contains(err.Error(), "native forwarding rule") {
		t.Fatal("downgraded native rule accepted", err)
	}
}

func TestNativeRuntimeCannotFallBackOrBeCreatedViaOrdinaryAPI(t *testing.T) {
	f, p := nativeRuntimeFixture(t)
	if _, err := f.store.RuntimeMaterialForNode(context.Background(), f.nodeID, f.bindingID); err == nil {
		t.Fatal("native import fell back to ordinary key")
	}
	request := requestFromNative(f.rule)
	request.Revision = 0
	if _, _, err := forwarding.NewService(f.store, time.Now).Create(context.Background(), "admin", request, "forge-native"); err == nil {
		t.Fatal("ordinary API accepted native creation")
	}
	b := f.store.vlessBindings[f.bindingID]
	b.Binding.ImportedFrom = nil
	f.store.vlessBindings[f.bindingID] = b
	s, _ := vlessruntime.NewService(f.store, time.Now)
	if _, _, err := s.BindImported(context.Background(), "admin", f.bindingID, f.nodeID, p.Config, "unproven-native"); err == nil {
		t.Fatal("unproven native binding accepted")
	}
}
