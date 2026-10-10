package memoryrepo

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
)

func TestAutomaticRealityRuleRoundTripAndIdentityProjection(t *testing.T) {
	fixture := newVLESSBundleFixture(t, true)
	s := fixture.store
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	defaults := forwarding.RealityDefaults{ServerName: "example.com", Destination: "example.com:443"}
	service := forwarding.NewService(s, func() time.Time { return now }, forwarding.WithRealityDefaults(defaults))
	request := forwarding.Request{
		Name: "automatic Reality", CustomerID: fixture.rule.CustomerID,
		EntryGroupID: fixture.rule.EntryGroupID, EgressMode: forwarding.EgressDirect,
		IngressProtocol: forwarding.IngressVLESSReality, Protocol: forwarding.ProtocolTCP,
		VLESSOutboundMode: forwarding.VLESSOutboundSOCKS5,
		VLESSSOCKS5Host:   "landing.example.test", VLESSSOCKS5Port: 1080,
		VLESSSOCKS5Username: "landing-user", VLESSSOCKS5Password: "landing-secret",
		ListenPort: 12001, Targets: fixture.rule.Targets, SelectionPolicy: forwarding.SelectionRoundRobin,
	}
	rule, replayed, err := service.CreateForAdministrator(ctx, "admin", request, "auto-reality-rule")
	if err != nil || replayed {
		t.Fatalf("create automatic rule: replay=%v err=%v", replayed, err)
	}
	privateKey := s.autoRealityKeys[rule.ID]
	if !vlessruntime.RealityKeyMatchesPublicKey(privateKey, rule.RealityPublicKey) || rule.RealityShortID == "" || rule.RealityServerName != defaults.ServerName {
		t.Fatal("generated public and private Reality parameters do not match")
	}
	replayedRule, replayed, err := service.CreateForAdministrator(ctx, "admin", request, "auto-reality-rule")
	if err != nil || !replayed || replayedRule.ID != rule.ID || s.autoRealityKeys[rule.ID] != privateKey {
		t.Fatalf("automatic rule replay changed persisted secrets: replay=%v err=%v", replayed, err)
	}
	unconfigured := forwarding.NewService(s, func() time.Time { return now })
	if replayedRule, replayed, err = unconfigured.CreateForAdministrator(ctx, "admin", request, "auto-reality-rule"); err != nil || !replayed || replayedRule.ID != rule.ID {
		t.Fatalf("automatic rule replay required current defaults: replay=%v err=%v", replayed, err)
	}
	if _, _, err := unconfigured.CreateForAdministrator(ctx, "admin", request, "auto-no-defaults"); err == nil {
		t.Fatal("new automatic rule without configured target was accepted")
	}
	copyRequest := request
	copyRequest.ListenPort = 12002
	copyRule, _, err := service.CreateForAdministrator(ctx, "admin", copyRequest, "auto-reality-copy")
	if err != nil || copyRule.RealityPublicKey == rule.RealityPublicKey || s.autoRealityKeys[copyRule.ID] == privateKey {
		t.Fatalf("copied rule reused Reality key material: %v", err)
	}
	copyRequest.Revision = copyRule.Revision
	copyRequest.RealityServerName = defaults.ServerName
	copyRequest.RealityDestination = defaults.Destination
	copyRequest.RealityPublicKey = rule.RealityPublicKey
	copyRequest.RealityShortID = copyRule.RealityShortID
	if _, _, err := service.UpdateForAdministrator(ctx, "admin", copyRule.ID, copyRequest, "auto-reality-rekey"); err != nil {
		t.Fatal(err)
	}
	if s.autoRealityKeys[copyRule.ID] != "" {
		t.Fatal("old automatic private key survived a public key change")
	}
	public, _ := json.Marshal(replayedRule)
	audits, _ := json.Marshal(s.AuditEvents())
	if strings.Contains(string(public), privateKey) || strings.Contains(string(audits), privateKey) {
		t.Fatal("private key leaked into public rule or audit")
	}
	request.Revision = rule.Revision
	request.Name = "automatic Reality edited"
	updated, replayed, err := unconfigured.UpdateForAdministrator(ctx, "admin", rule.ID, request, "auto-reality-edit")
	if err != nil || replayed || updated.RealityPublicKey != rule.RealityPublicKey || s.autoRealityKeys[rule.ID] != privateKey {
		t.Fatalf("blank Reality fields did not preserve existing material: replay=%v err=%v", replayed, err)
	}
	firstNode := s.nodes[fixture.nodeID]
	firstNode.CredentialHash = strings.Repeat("a", 64)
	s.nodes[fixture.nodeID] = firstNode
	s.nodesByCredential[firstNode.CredentialHash] = fixture.nodeID
	nodeID := "vless-node-two"
	secondHash := strings.Repeat("b", 64)
	s.nodes[nodeID] = nodes.Node{ID: nodeID, Name: nodeID, CredentialHash: secondHash, Capabilities: []string{"xray", "vless-reality"}}
	s.nodesByCredential[secondHash] = nodeID
	s.membersByGroup[rule.EntryGroupID][nodeID] = groups.Member{GroupID: rule.EntryGroupID, NodeID: nodeID, Weight: 1, CreatedAt: now, UpdatedAt: now}
	poolID := "pool-auto-reality"
	s.endpointPools[poolID] = endpoints.EndpointPool{
		ID: poolID, Name: "automatic VLESS", GroupID: rule.EntryGroupID,
		Mode: endpoints.ModeSingleServiceEndpoint, Protocol: "vless", Hostname: "entry.example.test", Port: 443,
		SelectionPolicy: endpoints.SelectionWeightedRoundRobin, MemberCount: 2,
		OwnerID: "admin", RuleID: rule.ID, CreatedAt: now, UpdatedAt: now,
	}
	s.endpointMembers[poolID] = map[string]endpoints.EndpointPoolMember{}
	for _, id := range []string{fixture.nodeID, nodeID} {
		s.endpointMembers[poolID][id] = endpoints.EndpointPoolMember{
			PoolID: poolID, GroupID: rule.EntryGroupID, NodeID: id,
			Weight: 1, State: endpoints.CandidateEligible, UpdatedAt: now,
		}
	}
	identity, err := vlessidentity.NewService(s, func() time.Time { return now }, nil)
	if err != nil {
		t.Fatal(err)
	}
	binding, _, err := identity.Provision(ctx, "admin", vlessidentity.ProvisionRequest{
		CustomerID: rule.CustomerID, ForwardingRuleID: rule.ID, EndpointPoolID: poolID,
	}, "auto-identity")
	if err != nil {
		t.Fatalf("provision automatic identity: %v", err)
	}
	for _, id := range []string{fixture.nodeID, nodeID} {
		material, err := s.RuntimeMaterialForNode(ctx, id, binding.ID)
		if err != nil || material.RealityPrivateKey != privateKey || material.CredentialUUID == "" {
			t.Fatalf("node %s did not receive automatic material: %v", id, err)
		}
		fragments, err := s.compileForwardingFragmentsLocked(id)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, fragment := range fragments {
			if strings.Contains(string(fragment.Config), privateKey) && strings.Contains(string(fragment.Config), material.CredentialUUID) {
				found = true
			}
		}
		if !found {
			t.Fatalf("node %s did not receive compiled automatic Reality fragment", id)
		}
	}
	raw, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil || restored.autoRealityKeys[rule.ID] != privateKey {
		t.Fatalf("automatic private key snapshot round trip failed: %v", err)
	}
	for _, id := range []string{fixture.nodeID, nodeID} {
		if _, err := restored.RuntimeMaterialForNode(ctx, id, binding.ID); err != nil {
			t.Fatalf("restored node %s lost automatic material: %v", id, err)
		}
	}
	restored.vlessRuntimeMaterials["revoked-auto-override"] = vlessruntime.Material{
		ID: "revoked-auto-override", BindingID: binding.ID, NodeID: nodeID,
		State: vlessruntime.StateRevoked,
	}
	if _, err := restored.RuntimeMaterialForNode(ctx, nodeID, binding.ID); err == nil {
		t.Fatal("revoked node material was bypassed by automatic Reality fallback")
	}
}

func TestDecodeSnapshotDropsOrphanedAutomaticRealityKey(t *testing.T) {
	fixture := newVLESSBundleFixture(t, true)
	node := fixture.store.nodes[fixture.nodeID]
	node.CredentialHash = strings.Repeat("a", 64)
	fixture.store.nodes[fixture.nodeID] = node
	fixture.store.nodesByCredential[node.CredentialHash] = fixture.nodeID
	fixture.store.autoRealityKeys["deleted-rule"] = "orphan-private-material"
	raw, err := fixture.store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatalf("orphan runtime key should not prevent restore: %v", err)
	}
	if _, exists := restored.autoRealityKeys["deleted-rule"]; exists {
		t.Fatal("orphan automatic Reality key survived snapshot restore")
	}
}
