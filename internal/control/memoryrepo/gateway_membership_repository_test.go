package memoryrepo

import (
	"context"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
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
	now := time.Now().UTC().Truncate(time.Microsecond)
	store := New(auth.Administrator{ID: "admin", Username: "admin", CreatedAt: now})
	store.forwardRules["rule"] = forwarding.Rule{
		ID: "rule", EntryGroupID: "group", Revision: 3, Protocol: forwarding.ProtocolTCP,
		IngressProtocol: forwarding.IngressVLESSReality, ListenPort: 443,
		Status: forwarding.StatusPendingActivation,
	}
	store.endpointPools["pool"] = endpoints.EndpointPool{ID: "pool", GroupID: "group", RuleID: "rule", Protocol: "vless", Port: 443}
	store.endpointMembers["pool"] = map[string]endpoints.EndpointPoolMember{
		"node": {PoolID: "pool", GroupID: "group", NodeID: "node", DialHost: "node.example.com", Weight: 1,
			State: endpoints.CandidateEligible, LastHealthReason: "address_changed"},
	}
	verified := now.Add(-time.Second)
	observation := gatewaymembership.ProtocolObservation{
		RuleID: "rule", NodeID: "node", RuleRevision: 3, NodeConfigGeneration: 1,
		ConfigSHA256: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		Protocol:     forwarding.IngressVLESSReality, DialHost: "node.example.com",
		VerifiedAt: verified, LeaseExpiresAt: verified.Add(endpoints.DefaultHealthTTL),
	}
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
