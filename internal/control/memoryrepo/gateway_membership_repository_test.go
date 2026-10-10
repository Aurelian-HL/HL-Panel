package memoryrepo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	provisioningvless "github.com/hongle/hl-panel/internal/provisioning/vless"
	"github.com/hongle/hl-panel/internal/routing/endpointrouter"
)

func TestGatewayMembershipProjectsCurrentRuleAndNodeReceiptAtomically(t *testing.T) {
	now := time.Now().UTC()
	store := New(auth.Administrator{ID: "admin", Username: "admin", CreatedAt: now})
	store.customers["customer"] = customers.Customer{ID: "customer", Disabled: true}
	store.forwardRules["rule"] = forwarding.Rule{
		ID: "rule", CustomerID: "customer", EntryGroupID: "group", Revision: 3,
		Protocol: forwarding.ProtocolTCP, IngressProtocol: forwarding.IngressVLESSReality,
		ListenPort: 443, RealityServerName: "example.com", RealityPublicKey: "public",
		RealityShortID: "12345678",
	}
	store.nodes["node"] = nodes.Node{ID: "node", DesiredGeneration: 4}
	store.membersByGroup["group"] = map[string]groups.Member{
		"node": {GroupID: "group", NodeID: "node", Weight: 1},
	}
	store.endpointPools["pool"] = endpoints.EndpointPool{
		ID: "pool", GroupID: "group", RuleID: "rule", Protocol: "vless", Port: 443,
	}
	store.endpointMembers["pool"] = map[string]endpoints.EndpointPoolMember{
		"node": {PoolID: "pool", GroupID: "group", NodeID: "node", DialHost: "node.example.com", Weight: 1},
	}

	state, err := store.GatewayMembershipState(context.Background(), "pool")
	if err != nil {
		t.Fatal(err)
	}
	if state.Rule.Status != forwarding.StatusCustomerDisabled || state.Rule.Revision != 3 ||
		len(state.Members) != 1 || state.Deployments["node"].RuleRevision != 3 ||
		state.Deployments["node"].Status.Reason != deploymentreceipts.ReasonRuleInactive ||
		len(state.ProtocolHealth) != 0 {
		t.Fatalf("gateway snapshot used stale rule status or invented readiness: %+v", state)
	}
}

func TestPublishProtocolHealthRestoresLeaseAfterAddressChange(t *testing.T) {
	store, observation := protocolPublicationFixture(t)
	verified := observation.VerifiedAt
	if err := store.PublishProtocolHealth(context.Background(), observation); err != nil {
		t.Fatal(err)
	}
	state, err := store.GatewayMembershipState(context.Background(), "pool")
	if err != nil {
		t.Fatal(err)
	}
	member := state.Members[0]
	if member.LastHealthAt == nil || !member.LastHealthAt.Equal(verified) || member.LastHealthReason != "protocol_probe" {
		t.Fatalf("protocol proof did not restore member lease: %+v", member)
	}
	if _, ok := state.ProtocolHealth["node"]; !ok {
		t.Fatal("protocol proof was not persisted")
	}
}

func protocolPublicationFixture(t *testing.T) (*Store, gatewaymembership.ProtocolObservation) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	store := New(auth.Administrator{ID: "admin", Username: "admin", CreatedAt: now})
	privateKey := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	publicKey, err := vlessruntime.RealityPublicKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	store.customers["customer"] = customers.Customer{ID: "customer", Username: "fixture"}
	store.deviceGroups["group"] = groups.DeviceGroup{ID: "group", Name: "fixture", Kind: groups.KindEntry}
	store.forwardRules["rule"] = forwarding.Rule{
		ID: "rule", CustomerID: "customer", EntryGroupID: "group", Revision: 3, Protocol: forwarding.ProtocolTCP,
		IngressProtocol: forwarding.IngressVLESSReality, ListenPort: 443,
		Status: forwarding.StatusPendingActivation, EgressMode: forwarding.EgressDirect, VLESSFlow: "xtls-rprx-vision", SendProxyProtocol: forwarding.SendProxyDisabled,
		RealityServerName: "example.org", RealityPublicKey: publicKey, RealityShortID: "abcd",
	}
	store.nodes["node"] = nodes.Node{ID: "node", DesiredGeneration: 1, AppliedGeneration: 1, Capabilities: []string{"xray"}}
	store.membersByGroup["group"] = map[string]groups.Member{"node": {GroupID: "group", NodeID: "node"}}
	raw, err := json.Marshal(agentv1.ConfigurationBundle{SchemaVersion: 1, Fragments: []agentv1.ConfigurationFragment{{
		GroupID: "forwarding-vless/rule", GroupRevision: 3, Engine: agentv1.EngineXray, Config: json.RawMessage(`{"inbounds":[]}`),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	configHash := generations.SHA256Hex(raw)
	store.nodeConfigsByNode["node"] = map[int64]generations.NodeConfigGeneration{1: {
		NodeID: "node", Generation: 1, Engine: agentv1.EngineNodeBundle, Config: raw, ConfigSHA256: configHash,
	}}
	const attemptID = "0123456789abcdef0123456789abcdef"
	store.applyAttemptsByNode["node"] = map[int64]generations.ApplyAttemptState{1: {CurrentID: attemptID}}
	store.applyResultsByNode["node"] = map[int64]map[string]generations.ApplyResult{1: {}}
	for _, phase := range []agentv1.ApplyPhase{agentv1.ApplyPhaseCommit, agentv1.ApplyPhaseVerify} {
		store.applyResultsByNode["node"][1][string(phase)] = generations.ApplyResult{
			NodeID: "node", Generation: 1, ConfigSHA256: configHash, Phase: phase,
			Status: agentv1.ApplyStatusSucceeded, EngineMode: "xray", AttemptID: attemptID,
		}
	}
	store.endpointPools["pool"] = endpoints.EndpointPool{ID: "pool", GroupID: "group", RuleID: "rule", Mode: endpoints.ModeSingleServiceEndpoint, Hostname: "entry.example.test", Protocol: "vless", Port: 443}
	store.endpointMembers["pool"] = map[string]endpoints.EndpointPoolMember{
		"node": {PoolID: "pool", GroupID: "group", NodeID: "node", DialHost: "node.example.com", Weight: 1,
			State: endpoints.CandidateEligible, LastHealthReason: "address_changed"},
	}
	verified := now.Add(-time.Second)
	const credential = "ed08862b-0b98-4a96-b860-503c97b78f55"
	store.vlessBindings["binding"] = vlessidentity.CredentialRecord{Binding: vlessidentity.Binding{
		ID: "binding", CustomerID: "customer", ForwardingRuleID: "rule", EndpointPoolID: "pool", State: vlessidentity.StateActive, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}, CredentialUUID: credential}
	store.vlessRuntimeMaterials["material"] = vlessruntime.Material{
		ID: "material", BindingID: "binding", NodeID: "node", CustomerID: "customer", ForwardingRuleID: "rule", EndpointPoolID: "pool", State: vlessruntime.StateActive,
		Revision: 1, CreatedAt: now, UpdatedAt: now, CredentialUUID: credential, RealityPrivateKey: privateKey,
	}
	observation := gatewaymembership.ProtocolObservation{
		RuleID: "rule", NodeID: "node", RuleRevision: 3, NodeConfigGeneration: 1,
		ConfigSHA256: configHash,
		Protocol:     forwarding.IngressVLESSReality, DialHost: "node.example.com",
		VerifiedAt: verified, LeaseExpiresAt: verified.Add(endpoints.DefaultHealthTTL),
	}
	return store, observation
}

func TestPublishProtocolHealthRejectsDelayedOrMismatchedProofWithoutOverwritingLease(t *testing.T) {
	cases := map[string]func(*gatewaymembership.ProtocolObservation){
		"delayed sample": func(o *gatewaymembership.ProtocolObservation) {
			o.VerifiedAt = o.VerifiedAt.Add(-time.Second)
			o.LeaseExpiresAt = o.LeaseExpiresAt.Add(-time.Second)
		},
		"same sample extending lease": func(o *gatewaymembership.ProtocolObservation) {
			o.LeaseExpiresAt = o.LeaseExpiresAt.Add(time.Second)
		},
		"old rule":       func(o *gatewaymembership.ProtocolObservation) { o.RuleRevision-- },
		"old generation": func(o *gatewaymembership.ProtocolObservation) { o.NodeConfigGeneration++ },
		"other config": func(o *gatewaymembership.ProtocolObservation) {
			o.ConfigSHA256 = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
		},
		"old address":    func(o *gatewaymembership.ProtocolObservation) { o.DialHost = "old.example.com" },
		"wrong protocol": func(o *gatewaymembership.ProtocolObservation) { o.Protocol = forwarding.IngressTCP },
		"future sample": func(o *gatewaymembership.ProtocolObservation) {
			o.VerifiedAt = time.Now().Add(time.Minute)
			o.LeaseExpiresAt = o.VerifiedAt.Add(endpoints.DefaultHealthTTL)
		},
		"expired sample": func(o *gatewaymembership.ProtocolObservation) {
			o.VerifiedAt = time.Now().Add(-2 * time.Minute)
			o.LeaseExpiresAt = o.VerifiedAt.Add(endpoints.DefaultHealthTTL)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			store, proof := protocolPublicationFixture(t)
			if err := store.PublishProtocolHealth(context.Background(), proof); err != nil {
				t.Fatal(err)
			}
			before := cloneEndpointMember(store.endpointMembers["pool"]["node"])
			invalid := proof
			mutate(&invalid)
			if err := store.PublishProtocolHealth(context.Background(), invalid); err == nil {
				t.Fatal("stale or mismatched probe overwrote current health")
			}
			if store.protocolHealth["pool"]["node"] != proof {
				t.Fatal("rejected probe changed the persisted observation")
			}
			after := store.endpointMembers["pool"]["node"]
			if !before.LastHealthAt.Equal(*after.LastHealthAt) || before.LastHealthReason != after.LastHealthReason || !before.UpdatedAt.Equal(after.UpdatedAt) {
				t.Fatal("rejected probe changed the member health lease")
			}
		})
	}
}

func TestPublishProtocolHealthRechecksChangedDeploymentBeforeWriting(t *testing.T) {
	cases := map[string]func(*Store){
		"rule paused":       func(s *Store) { rule := s.forwardRules["rule"]; rule.Paused = true; s.forwardRules["rule"] = rule },
		"customer disabled": func(s *Store) { s.customers["customer"] = customers.Customer{ID: "customer", Disabled: true} },
		"member retired": func(s *Store) {
			member := s.membersByGroup["group"]["node"]
			now := time.Now()
			member.RetiredAt = &now
			s.membersByGroup["group"]["node"] = member
		},
		"configuration pending": func(s *Store) { node := s.nodes["node"]; node.DesiredGeneration++; s.nodes["node"] = node },
		"apply invalidated": func(s *Store) {
			attempt := s.applyAttemptsByNode["node"][1]
			attempt.Invalidated = true
			s.applyAttemptsByNode["node"][1] = attempt
		},
		"verify failed": func(s *Store) {
			result := s.applyResultsByNode["node"][1][string(agentv1.ApplyPhaseVerify)]
			result.Status = agentv1.ApplyStatusFailed
			s.applyResultsByNode["node"][1][string(agentv1.ApplyPhaseVerify)] = result
		},
		"address changed": func(s *Store) {
			member := s.endpointMembers["pool"]["node"]
			member.DialHost = "new.example.com"
			s.endpointMembers["pool"]["node"] = member
		},
		"pool moved": func(s *Store) {
			pool := s.endpointPools["pool"]
			pool.GroupID = "other"
			s.endpointPools["pool"] = pool
		},
		"listener changed": func(s *Store) { pool := s.endpointPools["pool"]; pool.Port++; s.endpointPools["pool"] = pool },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			store, proof := protocolPublicationFixture(t)
			change(store)
			if err := store.PublishProtocolHealth(context.Background(), proof); err == nil {
				t.Fatal("probe of the previous deployment restored current health")
			}
			member := store.endpointMembers["pool"]["node"]
			if len(store.protocolHealth["pool"]) != 0 || member.LastHealthAt != nil || member.LastHealthReason != "address_changed" {
				t.Fatal("rejected probe partially changed health state")
			}
		})
	}
}

func TestPublishProtocolHealthAcceptsRetryAndCurrentAddressAfterClockCorrection(t *testing.T) {
	store, proof := protocolPublicationFixture(t)
	for i := 0; i < 2; i++ {
		if err := store.PublishProtocolHealth(context.Background(), proof); err != nil {
			t.Fatalf("valid probe retry failed: %v", err)
		}
	}
	newer := proof
	newer.VerifiedAt = proof.VerifiedAt.Add(100 * time.Millisecond)
	newer.LeaseExpiresAt = newer.VerifiedAt.Add(endpoints.DefaultHealthTTL)
	if err := store.PublishProtocolHealth(context.Background(), newer); err != nil {
		t.Fatal(err)
	}
	member := store.endpointMembers["pool"]["node"]
	member.DialHost = "new.example.com"
	store.endpointMembers["pool"]["node"] = member
	current := proof
	current.DialHost = member.DialHost
	current.VerifiedAt = proof.VerifiedAt.Add(-time.Second)
	current.LeaseExpiresAt = current.VerifiedAt.Add(endpoints.DefaultHealthTTL)
	if err := store.PublishProtocolHealth(context.Background(), current); err != nil {
		t.Fatalf("old address blocked valid new-address proof after clock correction: %v", err)
	}
	count, err := gatewaymembership.NewService(store).ReadyCandidateCount(context.Background(), "pool")
	if err != nil || count != 1 {
		t.Fatalf("valid current-address proof did not restore routing: count=%d err=%v", count, err)
	}
}

func TestProtocolHealthRenewalAdvancesGatewayRevision(t *testing.T) {
	store, proof := protocolPublicationFixture(t)
	service := gatewaymembership.NewService(store)
	router, err := endpointrouter.New(endpointrouter.SelectionPolicyWeightedRoundRobin)
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Snapshot(context.Background(), "pool")
	if err != nil {
		t.Fatal(err)
	}
	if err := router.ReplaceVersionedMembership(first.Revision, first.Endpoints); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := store.PublishProtocolHealth(context.Background(), proof); err != nil {
			t.Fatal(err)
		}
		next, err := service.Snapshot(context.Background(), "pool")
		if err != nil {
			t.Fatal(err)
		}
		if next.Revision <= first.Revision {
			t.Fatal("protocol health changed without advancing gateway revision")
		}
		if err := router.ReplaceVersionedMembership(next.Revision, next.Endpoints); err != nil {
			t.Fatal(err)
		}
		first = next
		proof.VerifiedAt = proof.VerifiedAt.Add(100 * time.Millisecond)
		proof.LeaseExpiresAt = proof.VerifiedAt.Add(endpoints.DefaultHealthTTL)
	}
}

func TestProtocolHealthAndGatewayRevisionSurviveSnapshotRestore(t *testing.T) {
	store, proof := protocolPublicationFixture(t)
	hash, err := auth.HashPassword("isolated-protocol-snapshot")
	if err != nil {
		t.Fatal(err)
	}
	admin := store.adminsByUsername["admin"]
	admin.PasswordHash = hash
	store.adminsByUsername["admin"] = admin
	customer := store.customers["customer"]
	customer.PasswordHash, customer.UserGroupID, customer.Revision = hash, "users", 1
	customer.CreatedAt, customer.UpdatedAt = admin.CreatedAt, admin.CreatedAt
	store.customers["customer"] = customer
	store.userGroups["users"] = customers.UserGroup{ID: "users", Name: "fixture users", Revision: 1,
		AllowedEntryGroupIDs: []string{"group"}, AllowDirect: true, CreatedAt: admin.CreatedAt, UpdatedAt: admin.CreatedAt}
	store.groupNetworks["group"] = groupconfig.GroupNetwork{GroupID: "group", ConnectHost: "entry.example.test",
		PortStart: 443, PortEnd: 443, AllowDirect: true, TrafficMultiplier: 1, Revision: 1}
	store.revisionByIdempotency["group"] = map[string]string{}
	rule := store.forwardRules["rule"]
	rule.Name, rule.SelectionPolicy = "fixture rule", forwarding.SelectionRoundRobin
	rule.VLESSOutboundMode, rule.VLESSSOCKS5Host, rule.VLESSSOCKS5Port = forwarding.VLESSOutboundSOCKS5, "landing.example.test", 1080
	store.forwardRules["rule"] = rule
	store.vlessSOCKS5Upstreams["rule"] = provisioningvless.SOCKS5Upstream{Hostname: rule.VLESSSOCKS5Host, Port: rule.VLESSSOCKS5Port, Username: "fixture", Password: "fixture"}
	node := store.nodes["node"]
	node.CredentialHash = "fixture-node-credential-hash"
	store.nodes["node"], store.nodesByCredential[node.CredentialHash] = node, node.ID
	attempt := store.applyAttemptsByNode["node"][1]
	attempt.SeenIDs = map[string]bool{attempt.CurrentID: true}
	store.applyAttemptsByNode["node"][1] = attempt
	ctx := context.Background()
	if err := store.PublishProtocolHealth(ctx, proof); err != nil {
		t.Fatal(err)
	}
	before, err := gatewaymembership.NewService(store).Snapshot(ctx, "pool")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	after, err := gatewaymembership.NewService(restored).Snapshot(ctx, "pool")
	if err != nil || after.Revision != before.Revision || len(after.Endpoints) != 1 || after.Endpoints[0] != before.Endpoints[0] {
		t.Fatal("restore changed the health lease or membership revision", err)
	}
	router, err := endpointrouter.New(endpointrouter.SelectionPolicyWeightedRoundRobin)
	if err != nil {
		t.Fatal(err)
	}
	if err := router.ReplaceVersionedMembership(before.Revision, before.Endpoints); err != nil {
		t.Fatal(err)
	}
	if err := router.ReplaceVersionedMembership(after.Revision, after.Endpoints); err != nil {
		t.Fatal(err)
	}
	proof.VerifiedAt = proof.VerifiedAt.Add(100 * time.Millisecond)
	proof.LeaseExpiresAt = proof.VerifiedAt.Add(endpoints.DefaultHealthTTL)
	if err := restored.PublishProtocolHealth(ctx, proof); err != nil {
		t.Fatal(err)
	}
	renewed, err := gatewaymembership.NewService(restored).Snapshot(ctx, "pool")
	if err != nil || renewed.Revision <= after.Revision {
		t.Fatal("restored gateway revision did not advance on renewal", err)
	}
	if err := router.ReplaceVersionedMembership(renewed.Revision, renewed.Endpoints); err != nil {
		t.Fatal(err)
	}
}

func TestMarkActivatedRequiresCurrentProtocolAndDeploymentEvidence(t *testing.T) {
	cases := map[string]func(*Store){
		"no proof": func(s *Store) { delete(s.protocolHealth, "pool") },
		"expired proof": func(s *Store) {
			proof := s.protocolHealth["pool"]["node"]
			proof.VerifiedAt = time.Now().Add(-2 * time.Minute)
			proof.LeaseExpiresAt = proof.VerifiedAt.Add(endpoints.DefaultHealthTTL)
			s.protocolHealth["pool"]["node"] = proof
		},
		"configuration changed": func(s *Store) { node := s.nodes["node"]; node.DesiredGeneration++; s.nodes["node"] = node },
		"apply invalidated": func(s *Store) {
			attempt := s.applyAttemptsByNode["node"][1]
			attempt.Invalidated = true
			s.applyAttemptsByNode["node"][1] = attempt
		},
		"member retired": func(s *Store) {
			member := s.membersByGroup["group"]["node"]
			now := time.Now()
			member.RetiredAt = &now
			s.membersByGroup["group"]["node"] = member
		},
		"address changed": func(s *Store) {
			member := s.endpointMembers["pool"]["node"]
			member.DialHost = "new.example.com"
			s.endpointMembers["pool"]["node"] = member
		},
		"customer disabled": func(s *Store) {
			customer := s.customers["customer"]
			customer.Disabled = true
			s.customers["customer"] = customer
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			store, proof := protocolPublicationFixture(t)
			if err := store.PublishProtocolHealth(context.Background(), proof); err != nil {
				t.Fatal(err)
			}
			change(store)
			before := len(store.auditEvents)
			if err := store.MarkActivated(context.Background(), "rule", 3, audit.Event{ID: "activation"}); err == nil {
				t.Fatal("changed deployment was marked active")
			}
			if store.forwardRules["rule"].Deployed || len(store.auditEvents) != before {
				t.Fatal("rejected activation changed rule or audit")
			}
		})
	}
	store, proof := protocolPublicationFixture(t)
	if err := store.PublishProtocolHealth(context.Background(), proof); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := store.MarkActivated(context.Background(), "rule", 3, audit.Event{ID: "activation"}); err != nil {
			t.Fatal(err)
		}
	}
	if !store.forwardRules["rule"].Deployed || len(store.auditEvents) != 1 {
		t.Fatal("valid activation was not idempotent")
	}
}
