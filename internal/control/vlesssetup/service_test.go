package vlesssetup

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

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

func (e *testEndpoints) FindPoolForRuleForAdministrator(context.Context, string, string) (endpoints.EndpointPool, error) {
	return e.pool, nil
}

func (e *testEndpoints) CreateBound(_ context.Context, _, name, groupID, ruleID string, mode endpoints.Mode, protocol, hostname string, port int, policy endpoints.SelectionPolicy, _ string) (endpoints.EndpointPool, bool, error) {
	e.creates++
	e.pool = endpoints.EndpointPool{ID: "pool-1", Name: name, GroupID: groupID, RuleID: ruleID, Mode: mode, Protocol: protocol, Hostname: hostname, Port: port, SelectionPolicy: policy}
	return e.pool, false, nil
}

type testIdentities struct {
	binding    vlessidentity.Binding
	provisions int
	failOnce   bool
}

func (i *testIdentities) List(context.Context, string) ([]vlessidentity.Binding, error) {
	if i.binding.ID == "" {
		return nil, nil
	}
	return []vlessidentity.Binding{i.binding}, nil
}

func (i *testIdentities) Provision(_ context.Context, _ string, request vlessidentity.ProvisionRequest, _ string) (vlessidentity.Binding, bool, error) {
	i.provisions++
	if i.failOnce {
		i.failOnce = false
		return vlessidentity.Binding{}, false, errors.New("transient identity write failure")
	}
	i.binding = vlessidentity.Binding{ID: "binding-1", CustomerID: request.CustomerID, ForwardingRuleID: request.ForwardingRuleID, EndpointPoolID: request.EndpointPoolID, State: vlessidentity.StateActive}
	return i.binding, false, nil
}

type testRules []forwarding.Rule

func (r testRules) List(ctx context.Context) ([]forwarding.Rule, error) { return r, ctx.Err() }

type blockingRules struct {
	started  chan struct{}
	returned chan error
}

func (r blockingRules) List(ctx context.Context) ([]forwarding.Rule, error) {
	close(r.started)
	<-ctx.Done()
	err := ctx.Err()
	r.returned <- err
	return nil, err
}

func TestReconcileResumesPartialSetupAndSkipsInactiveRules(t *testing.T) {
	endpointSource := &testEndpoints{}
	identities := &testIdentities{failOnce: true}
	service := NewService(testNetworks{groupconfig.GroupNetwork{ConnectHost: "edge.example.com"}}, endpointSource, identities)
	rule := readyRule()
	rule.Status = forwarding.StatusPendingActivation
	rule.ActivationReason = forwarding.ActivationReasonVLESSRuntimeMaterialPending
	if err := service.Ensure(context.Background(), "admin-1", rule); err == nil || endpointSource.creates != 1 {
		t.Fatal("fixture did not interrupt after committing endpoint")
	}
	for round := 0; round < 2; round++ {
		if err := service.Reconcile(context.Background(), testRules{rule}); err != nil {
			t.Fatal(err)
		}
	}
	if endpointSource.creates != 1 || identities.provisions != 2 || identities.binding.ID == "" {
		t.Fatal("partial setup was not recovered idempotently")
	}
	for _, mutate := range []func(*forwarding.Rule){
		func(r *forwarding.Rule) { r.Paused = true },
		func(r *forwarding.Rule) { r.OwnerKind = forwarding.OwnerCustomer },
		func(r *forwarding.Rule) { r.Status = forwarding.StatusActive },
		func(r *forwarding.Rule) { r.ActivationReason = "" },
		func(r *forwarding.Rule) { r.Status = forwarding.StatusQuotaExhausted },
	} {
		inactive := rule
		mutate(&inactive)
		otherIdentities := &testIdentities{}
		if err := NewService(testNetworks{}, &testEndpoints{}, otherIdentities).Reconcile(context.Background(), testRules{inactive}); err != nil || otherIdentities.provisions != 0 {
			t.Fatalf("inactive/initialized rule was reprovisioned: %v", err)
		}
	}
}

func TestReconcileResumesUDPDirectWhileNodesAwaitUpgrade(t *testing.T) {
	endpointSource := &testEndpoints{}
	identities := &testIdentities{failOnce: true}
	service := NewService(testNetworks{groupconfig.GroupNetwork{ConnectHost: "edge.example.com"}}, endpointSource, identities)
	rule := readyRule()
	rule.Protocol = forwarding.ProtocolUDP
	rule.VLESSFlow = ""
	rule.VLESSFlowExplicit = true
	rule.VLESSOutboundMode = forwarding.VLESSOutboundDirect
	rule.Status = forwarding.StatusPendingActivation
	rule.ActivationReason = forwarding.ActivationReasonUDPUnsupported
	if err := service.Reconcile(context.Background(), testRules{rule}); err == nil || endpointSource.creates != 1 {
		t.Fatal("fixture did not interrupt after committing the UDP endpoint")
	}
	if err := service.Reconcile(context.Background(), testRules{rule}); err != nil {
		t.Fatal(err)
	}
	if endpointSource.creates != 1 || identities.provisions != 2 || identities.binding.ID == "" {
		t.Fatal("UDP setup was not recovered idempotently while awaiting node upgrade")
	}
	other := rule
	other.VLESSOutboundMode = forwarding.VLESSOutboundSOCKS5
	if err := NewService(testNetworks{}, &testEndpoints{}, &testIdentities{}).Reconcile(context.Background(), testRules{other}); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileContinuesAfterOneRuleFailsAndRunStops(t *testing.T) {
	endpointSource := &testEndpoints{}
	identities := &testIdentities{failOnce: true}
	service := NewService(testNetworks{groupconfig.GroupNetwork{ConnectHost: "edge.example.com"}}, endpointSource, identities)
	rule := readyRule()
	rule.Status, rule.ActivationReason = forwarding.StatusPendingActivation, forwarding.ActivationReasonVLESSRuntimeMaterialPending
	if err := service.Reconcile(context.Background(), testRules{rule, rule}); err == nil || identities.binding.ID == "" {
		t.Fatal("failure prevented later setup work or was silently swallowed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { service.Run(ctx, testRules{rule}, slog.New(slog.NewTextHandler(io.Discard, nil))); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled setup loop did not stop")
	}
}

func TestRunBoundsReconcileRound(t *testing.T) {
	rules := blockingRules{started: make(chan struct{}), returned: make(chan error, 1)}
	service := NewService(testNetworks{}, &testEndpoints{}, &testIdentities{}, WithReconcileTimeout(10*time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		service.Run(ctx, rules, slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()
	select {
	case <-rules.started:
	case <-time.After(time.Second):
		t.Fatal("reconcile round did not start")
	}
	select {
	case err := <-rules.returned:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("reconcile context error = %v, want deadline exceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reconcile round was not bounded")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("bounded setup loop did not stop")
	}
}

func readyRule() forwarding.Rule {
	return forwarding.Rule{
		ID: "rule-1", Name: "line", OwnerKind: forwarding.OwnerAdministrator, OwnerID: "admin-1",
		CustomerID: forwarding.AdministratorSubjectID("admin-1"), EntryGroupID: "entry-1", ListenPort: 10000,
		IngressProtocol: forwarding.IngressVLESSReality, EgressMode: forwarding.EgressDirect,
		Protocol: forwarding.ProtocolTCP, VLESSFlow: "xtls-rprx-vision", RealityServerName: "example.com",
		VLESSOutboundMode: forwarding.VLESSOutboundSOCKS5, VLESSSOCKS5Host: "landing.example.test", VLESSSOCKS5Port: 1080,
		VLESSSOCKS5Username: "landing-user",
		RealityPublicKey:    "public", RealityShortID: "0123456789abcdef", RealityDestination: "example.com:443",
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
