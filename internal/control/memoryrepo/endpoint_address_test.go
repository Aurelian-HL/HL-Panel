package memoryrepo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/rulegroups"
)

func TestEndpointHostnameVariantsShareOneAllocation(t *testing.T) {
	ctx := context.Background()
	store := New(auth.Administrator{ID: "admin", Username: "admin"})
	group, err := groups.NewService(store, time.Now).Create(ctx, "admin", "entry", groups.KindEntry, endpoints.SelectionWeightedRoundRobin, "")
	if err != nil {
		t.Fatal(err)
	}
	service := endpoints.NewService(store, time.Now)
	first, replay, err := service.Create(ctx, "admin", "service", group.ID, endpoints.ModeSingleServiceEndpoint, "vless", "Entry.Example.Test.", 443, endpoints.SelectionWeightedRoundRobin, "key")
	if err != nil || replay || first.Hostname != "entry.example.test" {
		t.Fatal("endpoint hostname was not canonicalized")
	}
	second, replay, err := service.Create(ctx, "admin", "service", group.ID, endpoints.ModeSingleServiceEndpoint, "vless", "entry.example.test", 443, endpoints.SelectionWeightedRoundRobin, "key")
	if err != nil || !replay || second.ID != first.ID {
		t.Fatal("equivalent hostname did not replay same allocation")
	}
	_, _, err = service.Create(ctx, "admin", "another", group.ID, endpoints.ModeSingleServiceEndpoint, "vless", "ENTRY.EXAMPLE.TEST", 443, endpoints.SelectionWeightedRoundRobin, "other-key")
	if !errors.Is(err, faults.ErrConflict) {
		t.Fatal("hostname casing bypassed stable endpoint uniqueness")
	}
}

func TestBoundEndpointUsesConfiguredGroupAddressAndRulePort(t *testing.T) {
	ctx := context.Background()
	store := New(auth.Administrator{ID: "admin", Username: "admin"})
	group, err := groups.NewService(store, time.Now).Create(ctx, "admin", "entry", groups.KindEntry, endpoints.SelectionWeightedRoundRobin, "")
	if err != nil {
		t.Fatal(err)
	}
	store.groupNetworks[group.ID] = groupconfig.GroupNetwork{GroupID: group.ID, ConnectHost: "entry.example.test", PortStart: 10000, PortEnd: 20000, PortRanges: []groupconfig.PortRange{{Start: 10000, End: 20000}}}
	store.forwardRules["rule-1"] = forwarding.Rule{ID: "rule-1", OwnerKind: forwarding.OwnerAdministrator, OwnerID: "admin", CustomerID: forwarding.AdministratorSubjectID("admin"), EntryGroupID: group.ID, ListenPort: 12000, IngressProtocol: forwarding.IngressVLESSReality}
	service := endpoints.NewService(store, time.Now)
	create := func(host string, port int, key string) error {
		_, _, err := service.CreateBound(ctx, "admin", "service", group.ID, "rule-1", endpoints.ModeSingleServiceEndpoint, "vless", host, port, endpoints.SelectionWeightedRoundRobin, key)
		return err
	}
	if err := create("other.example.test", 12000, "wrong-host"); !errors.Is(err, faults.ErrValidation) {
		t.Fatalf("accepted an address outside the entry network: %v", err)
	}
	if err := create("entry.example.test", 12001, "wrong-port"); !errors.Is(err, faults.ErrValidation) {
		t.Fatalf("accepted a listener port different from the rule: %v", err)
	}
	if err := create("entry.example.test", 12000, "valid"); err != nil {
		t.Fatalf("rejected the configured address and rule port: %v", err)
	}
	store.groupNetworks[group.ID] = groupconfig.GroupNetwork{GroupID: group.ID, ConnectHost: "entry.example.test", PortRanges: []groupconfig.PortRange{{Start: 13000, End: 14000}}}
	if err := create("entry.example.test", 12000, "outside-range"); !errors.Is(err, faults.ErrValidation) {
		t.Fatalf("accepted a port outside the entry network: %v", err)
	}
}

func TestBoundEndpointSyncsAddressAndRejectsChangesThatBreakBinding(t *testing.T) {
	ctx := context.Background()
	store, request := forwardingRepositoryFixture(t)
	forwardingService := forwarding.NewService(store, nil)
	rule, _, err := forwardingService.CreateForAdministrator(ctx, "admin-test", request, "create-rule")
	if err != nil {
		t.Fatal(err)
	}
	endpointService := endpoints.NewService(store, nil)
	pool, _, err := endpointService.CreateBound(ctx, "admin-test", "entry service", rule.EntryGroupID, rule.ID, endpoints.ModeSingleServiceEndpoint, "tcp", "entry.example.test", rule.ListenPort, endpoints.SelectionWeightedRoundRobin, "create-pool")
	if err != nil {
		t.Fatal(err)
	}
	if pool.RuleID != rule.ID {
		t.Fatalf("endpoint lost forwarding rule binding: %+v", pool)
	}
	updated, _, err := groupconfig.NewService(store, nil).Update(ctx, "admin-test", rule.EntryGroupID, groupconfig.Request{ConnectHost: "other.example.test", PortStart: 12000, PortEnd: 12009, AllowDirect: true, AllowedExitGroupIDs: []string{"exit-1"}, TrafficMultiplier: 1, Revision: 1}, "change-address")
	if err != nil || updated.ConnectHost != "other.example.test" {
		t.Fatalf("network address change failed: %v", err)
	}
	changedPool, err := store.EndpointPool(ctx, pool.ID)
	if err != nil || changedPool.Hostname != "other.example.test" {
		t.Fatalf("bound endpoint hostname was not synchronized: %+v, %v", changedPool, err)
	}
	request.ListenPort = rule.ListenPort + 1
	request.Revision = rule.Revision
	_, _, err = forwardingService.UpdateForAdministrator(ctx, "admin-test", rule.ID, request, "change-port")
	if !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("rule change detached the endpoint: %v", err)
	}
	_, _, err = rulegroups.NewService(store, nil).BatchForAdministrator(ctx, "admin-test", rulegroups.BatchRequest{Operation: rulegroups.BatchDelete, RuleIDs: []string{rule.ID}, ExpectedRevisions: map[string]int64{rule.ID: rule.Revision}}, "delete-rule")
	if err != nil {
		t.Fatalf("rule deletion with unreferenced endpoint failed: %v", err)
	}
	if _, err := store.EndpointPool(ctx, pool.ID); !errors.Is(err, faults.ErrNotFound) {
		t.Fatalf("rule deletion left a dangling endpoint: %v", err)
	}
}

func TestRuleDeletionKeepsBoundEndpointWithActiveConnections(t *testing.T) {
	ctx := context.Background()
	store, request := forwardingRepositoryFixture(t)
	rule, _, err := forwarding.NewService(store, nil).CreateForAdministrator(ctx, "admin-test", request, "create-rule-active")
	if err != nil {
		t.Fatal(err)
	}
	pool, _, err := endpoints.NewService(store, nil).CreateBound(ctx, "admin-test", "entry service", rule.EntryGroupID, rule.ID, endpoints.ModeSingleServiceEndpoint, "tcp", "entry.example.test", rule.ListenPort, endpoints.SelectionWeightedRoundRobin, "create-pool-active")
	if err != nil {
		t.Fatal(err)
	}
	store.endpointMembers[pool.ID] = map[string]endpoints.EndpointPoolMember{"node": {PoolID: pool.ID, NodeID: "node", ActiveConnections: 1}}
	beforeAudit := len(store.AuditEvents())
	_, _, err = rulegroups.NewService(store, nil).BatchForAdministrator(ctx, "admin-test", rulegroups.BatchRequest{Operation: rulegroups.BatchDelete, RuleIDs: []string{rule.ID}, ExpectedRevisions: map[string]int64{rule.ID: rule.Revision}}, "delete-rule-active")
	if !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("active endpoint did not block rule deletion: %v", err)
	}
	if _, err := store.ForwardingRule(ctx, rule.ID); err != nil {
		t.Fatalf("blocked deletion removed the rule: %v", err)
	}
	if _, err := store.EndpointPool(ctx, pool.ID); err != nil {
		t.Fatalf("blocked deletion removed the endpoint: %v", err)
	}
	if len(store.AuditEvents()) != beforeAudit {
		t.Fatal("blocked deletion wrote an audit event")
	}
}

func TestBoundRuleHasOneStableEndpoint(t *testing.T) {
	ctx := context.Background()
	store, request := forwardingRepositoryFixture(t)
	rule, _, err := forwarding.NewService(store, nil).CreateForAdministrator(ctx, "admin-test", request, "create-rule-single-endpoint")
	if err != nil {
		t.Fatal(err)
	}
	service := endpoints.NewService(store, nil)
	if _, _, err := service.CreateBound(ctx, "admin-test", "entry service", rule.EntryGroupID, rule.ID,
		endpoints.ModeSingleServiceEndpoint, "tcp", "entry.example.test", rule.ListenPort,
		endpoints.SelectionWeightedRoundRobin, "create-first-endpoint"); err != nil {
		t.Fatalf("create first bound endpoint: %v", err)
	}
	if _, _, err := service.CreateBound(ctx, "admin-test", "second entry service", rule.EntryGroupID, rule.ID,
		endpoints.ModeSingleServiceEndpoint, "tcp", "entry.example.test", rule.ListenPort,
		endpoints.SelectionWeightedRoundRobin, "create-second-endpoint"); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("second stable endpoint was accepted: %v", err)
	}
}

func TestBoundSOCKS5EndpointMatchesOnlySOCKS5Rule(t *testing.T) {
	ctx := context.Background()
	store := New(auth.Administrator{ID: "admin"})
	group, err := groups.NewService(store, time.Now).Create(ctx, "admin", "entry", groups.KindEntry, endpoints.SelectionWeightedRoundRobin, "")
	if err != nil {
		t.Fatal(err)
	}
	store.groupNetworks[group.ID] = groupconfig.GroupNetwork{GroupID: group.ID, ConnectHost: "entry.example.test", PortRanges: []groupconfig.PortRange{{Start: 12000, End: 12099}}}
	store.forwardRules["socks-rule"] = forwarding.Rule{
		ID: "socks-rule", OwnerKind: forwarding.OwnerAdministrator, OwnerID: "admin",
		CustomerID: forwarding.AdministratorSubjectID("admin"), EntryGroupID: group.ID,
		ListenPort: 12000, IngressProtocol: forwarding.IngressSOCKS5, Protocol: forwarding.ProtocolTCP,
	}
	service := endpoints.NewService(store, time.Now)
	if _, _, err := service.CreateBound(ctx, "admin", "socks service", group.ID, "socks-rule", endpoints.ModeSingleServiceEndpoint,
		"socks5", "entry.example.test", 12000, endpoints.SelectionWeightedRoundRobin, "socks-pool"); err != nil {
		t.Fatalf("valid SOCKS5 endpoint binding rejected: %v", err)
	}
	store.forwardRules["tcp-rule"] = forwarding.Rule{
		ID: "tcp-rule", OwnerKind: forwarding.OwnerAdministrator, OwnerID: "admin",
		CustomerID: forwarding.AdministratorSubjectID("admin"), EntryGroupID: group.ID,
		ListenPort: 12001, IngressProtocol: forwarding.IngressTCP, Protocol: forwarding.ProtocolTCP,
	}
	if _, _, err := service.CreateBound(ctx, "admin", "wrong socks service", group.ID, "tcp-rule", endpoints.ModeSingleServiceEndpoint,
		"socks5", "entry.example.test", 12001, endpoints.SelectionWeightedRoundRobin, "wrong-socks-pool"); !errors.Is(err, faults.ErrValidation) {
		t.Fatalf("SOCKS5 endpoint bound to raw TCP rule: %v", err)
	}
}
