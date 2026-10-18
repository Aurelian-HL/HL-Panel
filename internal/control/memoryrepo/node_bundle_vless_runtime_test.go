package memoryrepo

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	agentconfig "github.com/hongle/hl-panel/internal/agent/config"
	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
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

func TestVLESSUDPDirectCapabilityRequiresCompatibleAgentAndCore(t *testing.T) {
	for _, test := range []struct {
		name, agentVersion, xrayVersion string
		capability                      bool
		want                            bool
	}{
		{"minimum release", "v0.1.117", "26.7.28", true, true},
		{"newer patch", "v0.1.118", "26.7.28", true, true},
		{"newer minor", "v0.2.0", "26.7.28", true, true},
		{"old agent claims capability", "v0.1.116", "26.7.28", true, false},
		{"wrong core", "v0.1.117", "26.9.25", true, false},
		{"unknown core", "v0.1.117", "unknown", true, false},
		{"missing capability", "v0.1.117", "26.7.28", false, false},
		{"missing version prefix", "0.1.117", "26.7.28", true, false},
		{"prerelease", "v0.1.117-rc1", "26.7.28", true, false},
		{"leading zero", "v0.1.0117", "26.7.28", true, false},
		{"overflow", "v0.1.18446744073709551616", "26.7.28", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			node := nodes.Node{AgentVersion: test.agentVersion, EngineVersions: map[string]string{"xray": test.xrayVersion}}
			if test.capability {
				node.Capabilities = []string{agentconfig.CapabilityXrayVLESSUDPDirect}
			}
			if got := nodeSupportsVLESSUDPDirect(node); got != test.want {
				t.Fatalf("UDP DIRECT support = %v, want %v", got, test.want)
			}
		})
	}
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
	if view.ActivationReason != forwarding.ActivationReasonNoAvailableNodes || view.Deployed {
		t.Fatalf("runtime material without a dial address/receipt granted readiness: %q", view.ActivationReason)
	}
	if fixture.store.forwardingBaseViewLocked(fixture.store.forwardRules[fixture.rule.ID]).ActivationReason != "" {
		t.Fatal("complete runtime material is still marked as missing")
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

func TestVLESSUDPDirectWaitsForNodeCapabilityWithoutChangingExistingTCPBundle(t *testing.T) {
	fixture := newVLESSBundleFixture(t, true)
	store := fixture.store
	ctx := context.Background()
	now := fixture.rule.CreatedAt.Add(time.Minute)
	oldNode := store.nodes[fixture.nodeID]
	oldNode.AgentVersion = "v0.1.111"
	oldNode.EngineVersions = map[string]string{"xray": "25.9.11"}
	oldNode.Capabilities = []string{agentconfig.CapabilityXrayVLESSReality}
	store.nodes[fixture.nodeID] = oldNode

	initial, created, err := store.compileNodeConfigLocked(fixture.nodeID, now)
	if err != nil || !created {
		t.Fatalf("compile existing TCP VLESS bundle: created=%v err=%v", created, err)
	}
	var initialBundle agentv1.ConfigurationBundle
	if err := json.Unmarshal(initial.Config, &initialBundle); err != nil {
		t.Fatal(err)
	}
	if len(initialBundle.Fragments) != 1 || initialBundle.Fragments[0].GroupID != generations.ForwardingFragmentID(agentv1.EngineXray, fixture.rule.ID) {
		t.Fatalf("unexpected initial TCP VLESS bundle: %#v", initialBundle.Fragments)
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
	request := forwarding.Request{
		Name: "vless-reality-udp-direct", CustomerID: fixture.rule.CustomerID,
		EntryGroupID: fixture.rule.EntryGroupID, EgressMode: forwarding.EgressDirect,
		IngressProtocol: forwarding.IngressVLESSReality, Protocol: forwarding.ProtocolUDP,
		VLESSOutboundMode: forwarding.VLESSOutboundDirect, ListenPort: 12001,
		RealityServerName: fixture.rule.RealityServerName, RealityPublicKey: publicKey,
		RealityShortID: "abcdef0123456789", VLESSFlowExplicit: true,
		Targets:         []forwarding.Target{{Host: "192.0.2.50", Port: 5353}},
		SelectionPolicy: forwarding.SelectionRoundRobin,
	}
	udpRule, replayed, err := forwarding.NewService(store, func() time.Time { return now }).CreateForAdministrator(ctx, "admin", request, "vless-udp-with-old-agent")
	if err != nil || replayed {
		t.Fatalf("create UDP VLESS rule: replay=%v err=%v", replayed, err)
	}
	poolID := "pool-vless-udp"
	store.endpointPools[poolID] = endpoints.EndpointPool{
		ID: poolID, Name: "stable-vless-udp", OwnerID: "admin", RuleID: udpRule.ID,
		GroupID: udpRule.EntryGroupID, Mode: endpoints.ModeSingleServiceEndpoint,
		Protocol: "vless", Hostname: "vless.example.test", Port: udpRule.ListenPort,
		SelectionPolicy: endpoints.SelectionWeightedRoundRobin,
		CreatedAt:       now, UpdatedAt: now, MemberCount: 1,
	}
	store.endpointMembers[poolID] = map[string]endpoints.EndpointPoolMember{
		fixture.nodeID: {PoolID: poolID, GroupID: udpRule.EntryGroupID, NodeID: fixture.nodeID,
			Weight: 1, State: endpoints.CandidateEligible, UpdatedAt: now},
	}
	bindingID := "binding-vless-udp"
	credential := "a83f29c5-280a-4ba6-870a-d02cd08350ed"
	store.vlessBindings[bindingID] = vlessidentity.CredentialRecord{
		Binding: vlessidentity.Binding{ID: bindingID, CustomerID: udpRule.CustomerID,
			ForwardingRuleID: udpRule.ID, EndpointPoolID: poolID,
			State: vlessidentity.StateActive, Revision: 1, CreatedAt: now, UpdatedAt: now},
		CredentialUUID: credential,
	}
	store.vlessRuntimeMaterials["material-vless-udp"] = vlessruntime.Material{
		ID: "material-vless-udp", BindingID: bindingID, NodeID: fixture.nodeID,
		CustomerID: udpRule.CustomerID, ForwardingRuleID: udpRule.ID, EndpointPoolID: poolID,
		State: vlessruntime.StateActive, Revision: 1, CreatedAt: now, UpdatedAt: now,
		CredentialUUID: credential, RealityPrivateKey: privateKey,
	}

	staged, created, err := store.compileNodeConfigLocked(fixture.nodeID, now)
	if err != nil || created || staged.Generation != initial.Generation || staged.ConfigSHA256 != initial.ConfigSHA256 || !bytes.Equal(staged.Config, initial.Config) {
		t.Fatalf("old node's TCP bundle changed when UDP rule was staged: created=%v err=%v", created, err)
	}
	view, err := forwarding.NewService(store, nil).Get(ctx, udpRule.ID)
	if err != nil || view.Deployed || view.Status != forwarding.StatusPendingActivation || view.ActivationReason != forwarding.ActivationReasonUDPUnsupported {
		t.Fatalf("old node's UDP rule must remain pending: status=%s deployed=%v reason=%s err=%v", view.Status, view.Deployed, view.ActivationReason, err)
	}
	for i, untrusted := range []nodes.Heartbeat{
		{AgentVersion: "v0.1.116", EngineVersions: map[string]string{"xray": "26.7.28"}, Capabilities: []string{agentconfig.CapabilityXrayVLESSReality, agentconfig.CapabilityXrayVLESSUDPDirect}},
		{AgentVersion: "v0.1.117", EngineVersions: map[string]string{"xray": "25.9.11"}, Capabilities: []string{agentconfig.CapabilityXrayVLESSReality, agentconfig.CapabilityXrayVLESSUDPDirect}},
	} {
		if _, err := store.UpdateHeartbeat(ctx, fixture.nodeID, untrusted, now.Add(time.Duration(i+1)*time.Second), projectionAudit("udp-untrusted-capability", now)); err != nil {
			t.Fatal(err)
		}
		unchanged, _, err := store.DesiredNodeConfig(ctx, fixture.nodeID)
		if err != nil || unchanged.Generation != initial.Generation || unchanged.ConfigSHA256 != initial.ConfigSHA256 || !bytes.Equal(unchanged.Config, initial.Config) {
			t.Fatalf("untrusted UDP capability changed the TCP bundle: generation=%d err=%v", unchanged.Generation, err)
		}
	}

	heartbeat := nodes.Heartbeat{
		AgentVersion: "v0.1.117", EngineVersions: map[string]string{"xray": "26.7.28"},
		Capabilities: []string{agentconfig.CapabilityXrayVLESSReality, agentconfig.CapabilityXrayVLESSUDPDirect},
	}
	if _, err := store.UpdateHeartbeat(ctx, fixture.nodeID, heartbeat, now.Add(3*time.Second), projectionAudit("udp-capability", now)); err != nil {
		t.Fatal(err)
	}
	updated, _, err := store.DesiredNodeConfig(ctx, fixture.nodeID)
	if err != nil || updated.Generation != initial.Generation+1 || updated.ConfigSHA256 == initial.ConfigSHA256 {
		t.Fatalf("capability heartbeat did not publish UDP bundle: generation=%d err=%v", updated.Generation, err)
	}
	var updatedBundle agentv1.ConfigurationBundle
	if err := json.Unmarshal(updated.Config, &updatedBundle); err != nil {
		t.Fatal(err)
	}
	if len(updatedBundle.Fragments) != 2 {
		t.Fatalf("new bundle has %d fragments, want existing TCP and new UDP VLESS", len(updatedBundle.Fragments))
	}
	fragments := make(map[string]agentv1.ConfigurationFragment, len(updatedBundle.Fragments))
	for _, fragment := range updatedBundle.Fragments {
		fragments[fragment.GroupID] = fragment
	}
	oldFragment, ok := fragments[generations.ForwardingFragmentID(agentv1.EngineXray, fixture.rule.ID)]
	if !ok || !bytes.Equal(oldFragment.Config, initialBundle.Fragments[0].Config) {
		t.Fatal("UDP activation changed the existing TCP VLESS fragment")
	}
	udpFragment, ok := fragments[generations.ForwardingFragmentID(agentv1.EngineXray, udpRule.ID)]
	if !ok || udpFragment.Engine != agentv1.EngineXray {
		t.Fatal("new UDP rule was not compiled as an Xray VLESS fragment")
	}
	var runtime struct {
		Inbounds []struct {
			Protocol       string `json:"protocol"`
			StreamSettings struct {
				Network  string `json:"network"`
				Security string `json:"security"`
			} `json:"streamSettings"`
		} `json:"inbounds"`
		Outbounds []struct {
			Protocol string `json:"protocol"`
			Settings struct {
				FinalRules []struct {
					Network string `json:"network"`
				} `json:"finalRules"`
			} `json:"settings"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(udpFragment.Config, &runtime); err != nil {
		t.Fatal(err)
	}
	if len(runtime.Inbounds) != 1 || runtime.Inbounds[0].Protocol != "vless" ||
		runtime.Inbounds[0].StreamSettings.Network != "tcp" || runtime.Inbounds[0].StreamSettings.Security != "reality" {
		t.Fatalf("UDP target changed customer ingress away from VLESS Reality over TCP: %#v", runtime.Inbounds)
	}
	udpDirect := false
	for _, outbound := range runtime.Outbounds {
		if outbound.Protocol == "freedom" && len(outbound.Settings.FinalRules) == 1 && outbound.Settings.FinalRules[0].Network == "udp" {
			udpDirect = true
		}
	}
	if !udpDirect {
		t.Fatal("new VLESS fragment lacks the UDP DIRECT outbound permission")
	}
	view, err = forwarding.NewService(store, nil).Get(ctx, udpRule.ID)
	if err != nil || view.Deployed || view.Status != forwarding.StatusPendingActivation {
		t.Fatalf("compiled UDP bundle was mistaken for verified deployment: status=%s deployed=%v err=%v", view.Status, view.Deployed, err)
	}
	if _, err := store.UpdateHeartbeat(ctx, fixture.nodeID, heartbeat, now.Add(4*time.Second), projectionAudit("udp-capability-repeat", now)); err != nil {
		t.Fatal(err)
	}
	repeated, _, err := store.DesiredNodeConfig(ctx, fixture.nodeID)
	if err != nil || repeated.Generation != updated.Generation || repeated.ConfigSHA256 != updated.ConfigSHA256 || !bytes.Equal(repeated.Config, updated.Config) {
		t.Fatalf("repeat heartbeat reissued the same bundle: generation=%d err=%v", repeated.Generation, err)
	}

	oldHeartbeat := nodes.Heartbeat{
		AgentVersion: "v0.1.111", EngineVersions: map[string]string{"xray": "25.9.11"},
		Capabilities: []string{agentconfig.CapabilityXrayVLESSReality},
	}
	if _, err := store.UpdateHeartbeat(ctx, fixture.nodeID, oldHeartbeat, now.Add(5*time.Second), projectionAudit("udp-capability-lost", now)); err != nil {
		t.Fatal(err)
	}
	rolledBack, _, err := store.DesiredNodeConfig(ctx, fixture.nodeID)
	if err != nil || rolledBack.Generation != updated.Generation+1 || rolledBack.ConfigSHA256 != initial.ConfigSHA256 || !bytes.Equal(rolledBack.Config, initial.Config) {
		t.Fatalf("capability loss did not remove only the UDP fragment: generation=%d err=%v", rolledBack.Generation, err)
	}
	view, err = forwarding.NewService(store, nil).Get(ctx, udpRule.ID)
	if err != nil || view.Deployed || view.ActivationReason != forwarding.ActivationReasonUDPUnsupported {
		t.Fatalf("capability loss did not return UDP rule to pending: deployed=%v reason=%s err=%v", view.Deployed, view.ActivationReason, err)
	}
}
