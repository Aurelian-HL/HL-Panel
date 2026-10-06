package memoryrepo

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
)

type vlessBundleFixture struct {
	store      *Store
	rule       forwarding.Rule
	bindingID  string
	credential string
	privateKey string
	publicKey  string
	nodeID     string
	materialID string
	endpointID string
}

func newVLESSBundleFixture(t *testing.T, withCapability bool) vlessBundleFixture {
	t.Helper()
	store, request := forwardingRepositoryFixture(t)
	store.adminsByUsername["vless-admin"] = auth.Administrator{
		ID: "admin", Username: "vless-admin",
		PasswordHash: append([]byte(nil), store.adminsByUsername["admin"].PasswordHash...),
	}
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	nodeID := "vless-node"
	credentialHash := strings.Repeat("v", 64)
	node := nodes.Node{ID: nodeID, Name: "vless-node", CredentialHash: credentialHash}
	if withCapability {
		node.Capabilities = []string{"xray", "vless-reality"}
	}
	store.nodes[nodeID] = node
	store.nodesByCredential[credentialHash] = nodeID
	store.membersByGroup[request.EntryGroupID][nodeID] = groups.Member{
		GroupID: request.EntryGroupID, NodeID: nodeID, Weight: 1, Priority: 0,
		CreatedAt: now, UpdatedAt: now,
	}
	endpointID := "pool-vless"
	store.endpointPools[endpointID] = endpoints.EndpointPool{
		ID: endpointID, Name: "stable-vless", GroupID: request.EntryGroupID,
		Mode: endpoints.ModeSingleServiceEndpoint, Protocol: "vless",
		Hostname: "vless.example.test", Port: 443,
		SelectionPolicy: endpoints.SelectionWeightedRoundRobin,
		CreatedAt:       now, UpdatedAt: now, MemberCount: 1,
	}
	store.endpointMembers[endpointID] = map[string]endpoints.EndpointPoolMember{
		nodeID: {PoolID: endpointID, GroupID: request.EntryGroupID, NodeID: nodeID,
			Weight: 1, Priority: 0, State: endpoints.CandidateEligible, UpdatedAt: now},
	}
	privateBytes := make([]byte, 32)
	if _, err := rand.Read(privateBytes); err != nil {
		t.Fatal(err)
	}
	privateKey := base64.RawURLEncoding.EncodeToString(privateBytes)
	publicKey, err := vlessruntime.RealityPublicKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	request.Name = "vless-reality-runtime"
	request.ListenPort = 12000
	request.IngressProtocol = forwarding.IngressVLESSReality
	request.VLESSOutboundMode = forwarding.VLESSOutboundSOCKS5
	request.VLESSSOCKS5Host = "landing.example.test"
	request.VLESSSOCKS5Port = 1080
	request.VLESSSOCKS5Username = "landing-user"
	request.VLESSSOCKS5Password = "landing-secret"
	request.VLESSFlow = "xtls-rprx-vision"
	request.RealityServerName = "www.example.com"
	request.RealityPublicKey = publicKey
	request.RealityShortID = "0123456789abcdef"
	rule, replayed, err := forwarding.NewService(store, func() time.Time { return now }).CreateForAdministrator(context.Background(), "admin", request, "vless-runtime-rule")
	if err != nil || replayed {
		t.Fatalf("create VLESS rule: replay=%v err=%v", replayed, err)
	}
	pool := store.endpointPools[endpointID]
	pool.OwnerID, pool.RuleID = "admin", rule.ID
	store.endpointPools[endpointID] = pool
	bindingID := "binding-vless"
	credential := "ed08862b-0b98-4a96-b860-503c97b78f55"
	store.vlessBindings[bindingID] = vlessidentity.CredentialRecord{
		Binding: vlessidentity.Binding{ID: bindingID, CustomerID: rule.CustomerID,
			ForwardingRuleID: rule.ID, EndpointPoolID: endpointID,
			State: vlessidentity.StateActive, Revision: 1,
			CreatedAt: now, UpdatedAt: now},
		CredentialUUID: credential,
	}
	materialID := "material-vless"
	store.vlessRuntimeMaterials[materialID] = vlessruntime.Material{
		ID: materialID, BindingID: bindingID, NodeID: nodeID,
		CustomerID: rule.CustomerID, ForwardingRuleID: rule.ID, EndpointPoolID: endpointID,
		State: vlessruntime.StateActive, Revision: 1, CreatedAt: now, UpdatedAt: now,
		CredentialUUID: credential, RealityPrivateKey: privateKey,
	}
	return vlessBundleFixture{store: store, rule: rule, bindingID: bindingID,
		credential: credential, privateKey: privateKey, publicKey: publicKey,
		nodeID: nodeID, materialID: materialID, endpointID: endpointID}
}

func TestVLESSRuntimeMaterialCompilesXrayFragmentWithoutPublicSecretLeak(t *testing.T) {
	fixture := newVLESSBundleFixture(t, true)
	fragments, err := fixture.store.compileForwardingFragmentsLocked(fixture.nodeID)
	if err != nil {
		t.Fatalf("compile forwarding fragments: %v", err)
	}
	if len(fragments) != 1 || fragments[0].Engine != "xray" || fragments[0].GroupID != generations.ForwardingFragmentID("xray", fixture.rule.ID) {
		t.Fatalf("unexpected VLESS fragments: %#v", fragments)
	}
	var config map[string]any
	if err := json.Unmarshal(fragments[0].Config, &config); err != nil {
		t.Fatal(err)
	}
	rawConfig := string(fragments[0].Config)
	if !strings.Contains(rawConfig, fixture.privateKey) || !strings.Contains(rawConfig, fixture.credential) {
		t.Fatal("compiled Xray fragment did not contain the authorized runtime material")
	}
	if config["inbounds"] == nil || config["outbounds"] == nil {
		t.Fatalf("compiled fragment missing Xray sections: %s", rawConfig)
	}
	view, err := forwarding.NewService(fixture.store, func() time.Time { return time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC) }).Get(context.Background(), fixture.rule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.ActivationReason != "" {
		t.Fatalf("fully provisioned VLESS rule remains pending: %q", view.ActivationReason)
	}
	list, err := fixture.store.ListForwardingRules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	publicRaw, _ := json.Marshal(list)
	auditRaw, _ := json.Marshal(fixture.store.AuditEvents())
	materialList, err := fixture.store.ListPublicRuntimeMaterials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	materialRaw, _ := json.Marshal(materialList)
	for label, raw := range map[string][]byte{"rule list": publicRaw, "audit": auditRaw, "runtime list": materialRaw} {
		if strings.Contains(string(raw), fixture.privateKey) || strings.Contains(string(raw), fixture.credential) {
			t.Fatalf("%s leaked confidential VLESS material", label)
		}
	}
}

func TestVLESSRuntimeMaterialRequiresNodeCapabilityAndActiveMaterial(t *testing.T) {
	withoutCapability := newVLESSBundleFixture(t, false)
	fragments, err := withoutCapability.store.compileForwardingFragmentsLocked(withoutCapability.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fragments) != 0 {
		t.Fatalf("node without Xray capability received fragments: %#v", fragments)
	}
	view, err := forwarding.NewService(withoutCapability.store, func() time.Time { return time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC) }).Get(context.Background(), withoutCapability.rule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.ActivationReason != forwarding.ActivationReasonVLESSRuntimeMaterialPending {
		t.Fatalf("missing node capability reason = %q", view.ActivationReason)
	}

	withoutMaterial := newVLESSBundleFixture(t, true)
	delete(withoutMaterial.store.vlessRuntimeMaterials, withoutMaterial.materialID)
	fragments, err = withoutMaterial.store.compileForwardingFragmentsLocked(withoutMaterial.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fragments) != 0 {
		t.Fatalf("VLESS rule without runtime material received fragments: %#v", fragments)
	}
	view, err = forwarding.NewService(withoutMaterial.store, func() time.Time { return time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC) }).Get(context.Background(), withoutMaterial.rule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.ActivationReason != forwarding.ActivationReasonVLESSRuntimeMaterialPending {
		t.Fatalf("missing runtime material reason = %q", view.ActivationReason)
	}
}

func TestVLESSRuntimeMaterialDoesNotBypassUnsupportedRuleOptions(t *testing.T) {
	fixture := newVLESSBundleFixture(t, true)
	rule := fixture.store.forwardRules[fixture.rule.ID]
	rule.ConnectionLimit = 2
	fixture.store.forwardRules[rule.ID] = rule
	fragments, err := fixture.store.compileForwardingFragmentsLocked(fixture.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fragments) != 0 {
		t.Fatalf("rule with unenforced connection limit received executable fragments: %#v", fragments)
	}
	view, err := forwarding.NewService(fixture.store, nil).Get(context.Background(), rule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.ActivationReason != forwarding.ActivationReasonAdvancedOptionsUnsupported {
		t.Fatalf("rule with unenforced connection limit reason = %q", view.ActivationReason)
	}
}

func TestVLESSRuntimeMaterialRevokedOrMismatchedBindingIsExcluded(t *testing.T) {
	fixture := newVLESSBundleFixture(t, true)
	material := fixture.store.vlessRuntimeMaterials[fixture.materialID]
	revokedAt := material.UpdatedAt.Add(time.Minute)
	material.State = vlessruntime.StateRevoked
	material.RevokedAt = &revokedAt
	material.Revision++
	material.CredentialUUID = ""
	material.RealityPrivateKey = ""
	fixture.store.vlessRuntimeMaterials[fixture.materialID] = material
	fragments, err := fixture.store.compileForwardingFragmentsLocked(fixture.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fragments) != 0 {
		t.Fatalf("revoked runtime material received fragments: %#v", fragments)
	}

	fixture = newVLESSBundleFixture(t, true)
	material = fixture.store.vlessRuntimeMaterials[fixture.materialID]
	material.BindingID = "different-binding"
	fixture.store.vlessRuntimeMaterials[fixture.materialID] = material
	fragments, err = fixture.store.compileForwardingFragmentsLocked(fixture.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fragments) != 0 {
		t.Fatalf("mismatched runtime binding received fragments: %#v", fragments)
	}
}

func TestVLESSIdentityRevokeInvalidatesAllNodeRuntimeMaterial(t *testing.T) {
	fixture := newVLESSBundleFixture(t, true)
	now := fixture.store.vlessBindings[fixture.bindingID].Binding.UpdatedAt.Add(time.Minute)
	event, err := audit.NewEvent(now, "administrator", "admin", "vless_identity.revoke", "vless_identity_binding", fixture.bindingID, "succeeded", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, replayed, err := fixture.store.Revoke(context.Background(), vlessidentity.RevokeInput{
		BindingID: fixture.bindingID, ExpectedRevision: 1, IdempotencyKey: "revoke-runtime-material",
		RequestSHA256: strings.Repeat("a", 64), UpdatedBy: "admin", UpdatedAt: now,
	}, event)
	if err != nil || replayed {
		t.Fatalf("revoke identity: replay=%v err=%v", replayed, err)
	}
	material := fixture.store.vlessRuntimeMaterials[fixture.materialID]
	if material.State != vlessruntime.StateRevoked || material.CredentialUUID != "" || material.RealityPrivateKey != "" {
		t.Fatalf("runtime material was not fully revoked: %#v", material)
	}
	fragments, err := fixture.store.compileForwardingFragmentsLocked(fixture.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fragments) != 0 {
		t.Fatalf("revoked identity still produced executable fragments: %#v", fragments)
	}
}

func TestVLESSAndGOSTFragmentsCanShareNodeBundle(t *testing.T) {
	fixture := newVLESSBundleFixture(t, true)
	// Add a second ordinary TCP rule on the same entry group. The VLESS rule
	// keeps its Xray fragment while the existing GOST compiler remains active.
	request := forwarding.Request{Name: "gost-direct", CustomerID: fixture.rule.CustomerID,
		EntryGroupID: "entry-1", EgressMode: forwarding.EgressDirect, Protocol: forwarding.ProtocolTCP,
		ListenPort: 12001, Targets: []forwarding.Target{{Host: "landing.example.test", Port: 1080}},
		SelectionPolicy: forwarding.SelectionRoundRobin}
	if _, _, err := forwarding.NewService(fixture.store, func() time.Time { return time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC) }).CreateForAdministrator(context.Background(), "admin", request, "gost-with-vless"); err != nil {
		t.Fatalf("create GOST rule: %v", err)
	}
	fragments, err := fixture.store.compileForwardingFragmentsLocked(fixture.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fragments) != 2 {
		t.Fatalf("mixed node bundle has %d fragments, want 2: %#v", len(fragments), fragments)
	}
	engines := map[string]bool{}
	for _, fragment := range fragments {
		engines[string(fragment.Engine)] = true
	}
	if !engines["xray"] || !engines["gost"] {
		t.Fatalf("mixed bundle engines = %#v", engines)
	}
}
