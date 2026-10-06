package vlesssetup

import (
	"context"
	"testing"

	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
)

type testNetworks struct{ network groupconfig.GroupNetwork }

func (n testNetworks) Get(context.Context, string) (groupconfig.GroupNetwork, error) {
	return n.network, nil
}

type testEndpoints struct {
	pool    endpoints.EndpointPool
	creates int
}

func (e *testEndpoints) ListPoolsForAdministrator(context.Context, string) ([]endpoints.EndpointPool, error) {
	if e.pool.ID == "" {
		return nil, nil
	}
	return []endpoints.EndpointPool{e.pool}, nil
}

func (e *testEndpoints) CreateBound(_ context.Context, _, name, groupID, ruleID string, mode endpoints.Mode, protocol, hostname string, port int, policy endpoints.SelectionPolicy, _ string) (endpoints.EndpointPool, bool, error) {
	e.creates++
	e.pool = endpoints.EndpointPool{ID: "pool-1", Name: name, GroupID: groupID, RuleID: ruleID, Mode: mode, Protocol: protocol, Hostname: hostname, Port: port, SelectionPolicy: policy}
	return e.pool, false, nil
}

type testIdentities struct {
	binding    vlessidentity.Binding
	provisions int
}

func (i *testIdentities) List(context.Context, string) ([]vlessidentity.Binding, error) {
	if i.binding.ID == "" {
		return nil, nil
	}
	return []vlessidentity.Binding{i.binding}, nil
}

func (i *testIdentities) Provision(_ context.Context, _ string, request vlessidentity.ProvisionRequest, _ string) (vlessidentity.Binding, bool, error) {
	i.provisions++
	i.binding = vlessidentity.Binding{ID: "binding-1", CustomerID: request.CustomerID, ForwardingRuleID: request.ForwardingRuleID, EndpointPoolID: request.EndpointPoolID, State: vlessidentity.StateActive}
	return i.binding, false, nil
}

func readyRule() forwarding.Rule {
	return forwarding.Rule{
		ID: "rule-1", Name: "line", OwnerKind: forwarding.OwnerAdministrator, OwnerID: "admin-1",
		CustomerID: forwarding.AdministratorSubjectID("admin-1"), EntryGroupID: "entry-1", ListenPort: 10000,
		IngressProtocol: forwarding.IngressVLESSReality, EgressMode: forwarding.EgressDirect,
		Protocol: forwarding.ProtocolTCP, VLESSFlow: "xtls-rprx-vision", RealityServerName: "example.com",
		VLESSOutboundMode: forwarding.VLESSOutboundSOCKS5, VLESSSOCKS5Host: "landing.example.test", VLESSSOCKS5Port: 1080,
		VLESSSOCKS5Username: "landing-user",
		RealityPublicKey: "public", RealityShortID: "0123456789abcdef", RealityDestination: "example.com:443",
	}
}

func TestEnsureCreatesOnceAndResumes(t *testing.T) {
	endpointsSource := &testEndpoints{}
	identities := &testIdentities{}
	service := NewService(testNetworks{groupconfig.GroupNetwork{ConnectHost: "edge.example.com"}}, endpointsSource, identities)
	for attempt := 0; attempt < 2; attempt++ {
		if err := service.Ensure(context.Background(), "admin-1", readyRule()); err != nil {
			t.Fatal(err)
		}
	}
	if endpointsSource.creates != 1 || identities.provisions != 1 {
		t.Fatalf("replay created duplicate resources: endpoints=%d identities=%d", endpointsSource.creates, identities.provisions)
	}
	if endpointsSource.pool.Hostname != "edge.example.com" || endpointsSource.pool.Port != 10000 || endpointsSource.pool.Mode != endpoints.ModeSingleServiceEndpoint {
		t.Fatalf("wrong stable endpoint: %+v", endpointsSource.pool)
	}
}

func TestEnsureRejectsDifferentOwnerAndMismatchedEndpoint(t *testing.T) {
	endpointsSource := &testEndpoints{pool: endpoints.EndpointPool{ID: "pool-1", RuleID: "rule-1", GroupID: "entry-1", Protocol: "vless", Hostname: "other.example.com", Port: 10000}}
	identities := &testIdentities{}
	service := NewService(testNetworks{groupconfig.GroupNetwork{ConnectHost: "edge.example.com"}}, endpointsSource, identities)
	if err := service.Ensure(context.Background(), "other-admin", readyRule()); err == nil {
		t.Fatal("foreign administrator was accepted")
	}
	if err := service.Ensure(context.Background(), "admin-1", readyRule()); err == nil {
		t.Fatal("mismatched endpoint was accepted")
	}
	if identities.provisions != 0 {
		t.Fatal("identity was provisioned for mismatched endpoint")
	}
}
