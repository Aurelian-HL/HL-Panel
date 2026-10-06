package memoryrepo

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

func forwardingRepositoryFixture(t *testing.T) (*Store, forwarding.Request) {
	t.Helper()
	s, customer := customerRepositoryFixture(t)
	s.customers[customer.ID] = customer
	s.groupNetworks["entry-1"] = groupconfig.GroupNetwork{GroupID: "entry-1", ConnectHost: "entry.example.test", PortStart: 12000, PortEnd: 12009, AllowDirect: true, AllowedExitGroupIDs: []string{"exit-1"}, TrafficMultiplier: 1, Revision: 1}
	s.groupNetworks["exit-1"] = groupconfig.GroupNetwork{GroupID: "exit-1", DirectPolicy: groupconfig.DirectPolicyDisabled, TrafficMultiplier: 1, Revision: 1}
	s.nodes["exit-node"] = nodes.Node{ID: "exit-node", Name: "exit-node", CredentialHash: "exit-node-credential-hash"}
	s.nodesByCredential["exit-node-credential-hash"] = "exit-node"
	s.membersByGroup["exit-1"]["exit-node"] = groups.Member{GroupID: "exit-1", NodeID: "exit-node", DialHost: "exit.example.test", Weight: 1}
	return s, forwarding.Request{Name: "forward", CustomerID: customer.ID, EntryGroupID: "entry-1", EgressMode: forwarding.EgressDirect, Protocol: forwarding.ProtocolTCP, Targets: []forwarding.Target{{Host: "landing.example.test", Port: 1080}}, SelectionPolicy: forwarding.SelectionRoundRobin}
}

func TestAdministratorRuleHasSeparateIdempotencyAndSurvivesSnapshot(t *testing.T) {
	s, request := forwardingRepositoryFixture(t)
	service := forwarding.NewService(s, nil)
	ctx := context.Background()
	legacy, _, err := service.Create(ctx, "admin-test", request, "shared-key")
	if err != nil {
		t.Fatal(err)
	}
	adminRule, replayed, err := service.CreateForAdministrator(ctx, "admin-test", request, "shared-key")
	if err != nil || replayed || adminRule.ID == legacy.ID || !adminRule.OwnedByAdministrator("admin-test") {
		t.Fatalf("administrator rule collided with legacy idempotency: %+v replayed=%v err=%v", adminRule, replayed, err)
	}
	if _, err := service.GetForAdministrator(ctx, "admin-test", legacy.ID); !errors.Is(err, faults.ErrNotFound) {
		t.Fatalf("legacy customer rule leaked into administrator scope: %v", err)
	}
	raw, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatalf("administrator rule snapshot failed to restore: %v", err)
	}
	owned, err := forwarding.NewService(restored, nil).GetForAdministrator(ctx, "admin-test", adminRule.ID)
	if err != nil || !owned.OwnedByAdministrator("admin-test") {
		t.Fatalf("administrator ownership was lost after restore: %+v %v", owned, err)
	}
}

func TestForwardingConcurrentPortAllocationAndIdempotency(t *testing.T) {
	s, request := forwardingRepositoryFixture(t)
	service := forwarding.NewService(s, nil)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan forwarding.Rule, 10)
	errorsOut := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rule, _, err := service.Create(ctx, "admin", request, fmt.Sprintf("create-%d", i))
			if err != nil {
				errorsOut <- err
				return
			}
			results <- rule
		}(i)
	}
	wg.Wait()
	close(results)
	close(errorsOut)
	for err := range errorsOut {
		t.Fatal(err)
	}
	ports := map[int]bool{}
	for rule := range results {
		if ports[rule.ListenPort] {
			t.Fatal("concurrent requests reserved same port")
		}
		ports[rule.ListenPort] = true
	}
	if len(ports) != 10 {
		t.Fatalf("allocated %d ports", len(ports))
	}
	if _, _, err := service.Create(ctx, "admin", request, "range-exhausted"); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("port exhaustion: %v", err)
	}
	replayed, replay, err := service.Create(ctx, "admin", request, "create-0")
	if err != nil || !replay || !ports[replayed.ListenPort] || len(s.auditEvents) != 10 {
		t.Fatalf("replay: %v", err)
	}
	request.Name = "changed"
	if _, _, err := service.Create(ctx, "admin", request, "create-0"); !errors.Is(err, faults.ErrIdempotencyConflict) {
		t.Fatalf("payload conflict: %v", err)
	}
}

func TestForwardingRuleProjectsDefaultNetworkForPopulatedEntryGroup(t *testing.T) {
	s, request := forwardingRepositoryFixture(t)
	delete(s.groupNetworks, request.EntryGroupID)
	s.nodes["entry-node"] = nodes.Node{ID: "entry-node", Hostname: "entry.example.test", CredentialHash: "entry-node-credential-hash"}
	s.nodesByCredential["entry-node-credential-hash"] = "entry-node"
	s.membersByGroup[request.EntryGroupID]["entry-node"] = groups.Member{
		GroupID: request.EntryGroupID, NodeID: "entry-node", Weight: 100,
		DialHost: "entry.example.test",
	}

	rule, _, err := forwarding.NewService(s, nil).Create(context.Background(), "admin", request, "default-network")
	if err != nil {
		t.Fatalf("rule creation with a populated unconfigured group: %v", err)
	}
	if rule.ListenPort != defaultForwardingPortStart {
		t.Fatalf("default listener port = %d, want %d", rule.ListenPort, defaultForwardingPortStart)
	}
	network, exists := s.groupNetworks[request.EntryGroupID]
	if !exists || network.ConnectHost != "entry.example.test" || network.DirectPolicy != groupconfig.DirectPolicyOptional || network.Revision != 1 {
		t.Fatalf("default network projection = %+v, exists=%v", network, exists)
	}
	encoded, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(encoded)
	if err != nil {
		t.Fatalf("snapshot rejected projected network: %v", err)
	}
	if _, exists := restored.groupNetworks[request.EntryGroupID]; !exists {
		t.Fatal("projected network was not persisted in the snapshot")
	}
}

func TestForwardingRuleDoesNotProjectNetworkWithoutValidMember(t *testing.T) {
	s, request := forwardingRepositoryFixture(t)
	delete(s.groupNetworks, request.EntryGroupID)
	if _, _, err := forwarding.NewService(s, nil).Create(context.Background(), "admin", request, "missing-member-network"); !errors.Is(err, faults.ErrValidation) {
		t.Fatalf("unpopulated group was accepted: %v", err)
	}
	if _, exists := s.groupNetworks[request.EntryGroupID]; exists {
		t.Fatal("failed rule creation left an automatic network policy behind")
	}
}

func TestForwardingAuthorizationQuotasStatusAndPause(t *testing.T) {
	s, request := forwardingRepositoryFixture(t)
	service := forwarding.NewService(s, nil)
	ctx := context.Background()
	userGroup := s.userGroups["ugrp-1"]
	userGroup.AllowDirect = false
	s.userGroups[userGroup.ID] = userGroup
	if _, _, err := service.Create(ctx, "admin", request, "unauthorized"); !errors.Is(err, faults.ErrValidation) {
		t.Fatalf("direct authorization: %v", err)
	}
	userGroup.AllowDirect = true
	s.userGroups[userGroup.ID] = userGroup
	customer := s.customers[request.CustomerID]
	customer.MaxRules = 1
	s.customers[customer.ID] = customer
	rule, _, err := service.Create(ctx, "admin", request, "first")
	if err != nil || rule.Deployed || rule.Status != forwarding.StatusPendingActivation {
		t.Fatalf("honest status: %v", err)
	}
	request.Protocol = forwarding.ProtocolUDP
	if _, _, err := service.Create(ctx, "admin", request, "quota"); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("rule quota: %v", err)
	}
	request.Protocol = forwarding.ProtocolTCP
	request.Revision = rule.Revision
	customer.Disabled = true
	s.customers[customer.ID] = customer
	view, _ := service.Get(ctx, rule.ID)
	if view.Status != forwarding.StatusCustomerDisabled {
		t.Fatal("disabled customer was not reflected in rule status")
	}
	if _, _, err := service.Update(ctx, "admin", rule.ID, request, "disabled-resume"); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("disabled resume: %v", err)
	}
	request.Paused = true
	paused, _, err := service.Update(ctx, "admin", rule.ID, request, "pause-disabled")
	if err != nil || paused.Status != forwarding.StatusPaused || paused.ListenPort != rule.ListenPort {
		t.Fatalf("pause: %v", err)
	}
	if _, replay, err := service.Update(ctx, "admin", rule.ID, request, "pause-disabled"); err != nil || !replay {
		t.Fatalf("update replay: %v", err)
	}
	if _, _, err := service.Update(ctx, "admin", rule.ID, request, "stale-update"); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("stale update: %v", err)
	}
	customer.Disabled = false
	expires := time.Now().Add(-time.Second)
	customer.ExpiresAt = &expires
	s.customers[customer.ID] = customer
	request.Paused = false
	request.Revision = paused.Revision
	if _, _, err := service.Update(ctx, "admin", rule.ID, request, "expired-resume"); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("expired resume: %v", err)
	}
}

func TestForwardingExitAuthorizationAndPortProtocolIsolation(t *testing.T) {
	s, request := forwardingRepositoryFixture(t)
	service := forwarding.NewService(s, nil)
	ctx := context.Background()
	request.EgressMode = forwarding.EgressExitGroup
	request.ExitGroupID = "exit-1"
	request.ListenPort = 12000
	rule, _, err := service.Create(ctx, "admin", request, "exit-rule")
	if err != nil {
		t.Fatal(err)
	}
	request.Protocol = forwarding.ProtocolUDP
	if _, _, err = service.Create(ctx, "admin", request, "udp-same-port"); err != nil {
		t.Fatalf("TCP/UDP independent port namespaces: %v", err)
	}
	request.Protocol = forwarding.ProtocolTCP
	if _, _, err = service.Create(ctx, "admin", request, "same-port"); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("duplicate port: %v", err)
	}
	item, _ := service.Get(ctx, rule.ID)
	item.Targets[0].Host = "tampered.example.test"
	stored, _ := service.Get(ctx, rule.ID)
	if stored.Targets[0].Host == item.Targets[0].Host {
		t.Fatal("target slice escaped the lock")
	}
	network := s.groupNetworks[request.EntryGroupID]
	network.AllowedExitGroupIDs = []string{}
	s.groupNetworks[network.GroupID] = network
	request.ListenPort = 12001
	if _, _, err = service.Create(ctx, "admin", request, "disallowed-exit"); !errors.Is(err, faults.ErrValidation) {
		t.Fatalf("entry network authorization: %v", err)
	}
}

func TestForwardingDirectPolicyEnforcesRouteShape(t *testing.T) {
	ctx := context.Background()

	forcedStore, directRequest := forwardingRepositoryFixture(t)
	forced := forcedStore.groupNetworks[directRequest.EntryGroupID]
	forced.DirectPolicy = groupconfig.DirectPolicyForced
	forced.AllowDirect = true
	forced.AllowedExitGroupIDs = nil
	forcedStore.groupNetworks[forced.GroupID] = forced
	exitRequest := directRequest
	exitRequest.EgressMode = forwarding.EgressExitGroup
	exitRequest.ExitGroupID = "exit-1"
	if _, _, err := forwarding.NewService(forcedStore, nil).Create(ctx, "admin", exitRequest, "forced-exit"); !errors.Is(err, faults.ErrValidation) {
		t.Fatalf("FORCED entry accepted an exit route: %v", err)
	}
	if _, _, err := forwarding.NewService(forcedStore, nil).Create(ctx, "admin", directRequest, "forced-direct"); err != nil {
		t.Fatalf("FORCED entry rejected a direct route: %v", err)
	}

	disabledStore, disabledRequest := forwardingRepositoryFixture(t)
	disabled := disabledStore.groupNetworks[disabledRequest.EntryGroupID]
	disabled.DirectPolicy = groupconfig.DirectPolicyDisabled
	disabled.AllowDirect = false
	disabledStore.groupNetworks[disabled.GroupID] = disabled
	if _, _, err := forwarding.NewService(disabledStore, nil).Create(ctx, "admin", disabledRequest, "disabled-direct"); !errors.Is(err, faults.ErrValidation) {
		t.Fatalf("DISABLED entry accepted a direct route: %v", err)
	}
	disabledRequest.EgressMode = forwarding.EgressExitGroup
	disabledRequest.ExitGroupID = "exit-1"
	if _, _, err := forwarding.NewService(disabledStore, nil).Create(ctx, "admin", disabledRequest, "disabled-exit"); err != nil {
		t.Fatalf("DISABLED entry rejected an authorized exit route: %v", err)
	}
}

func TestGroupNetworkProtectsExistingRulesAndRevision(t *testing.T) {
	s, request := forwardingRepositoryFixture(t)
	ctx := context.Background()
	if _, _, err := forwarding.NewService(s, nil).Create(ctx, "admin", request, "first"); err != nil {
		t.Fatal(err)
	}
	service := groupconfig.NewService(s, nil)
	input := groupconfig.Request{ConnectHost: "new.example.test", PortStart: 12001, PortEnd: 12010, AllowDirect: true, AllowedExitGroupIDs: []string{"exit-1"}, TrafficMultiplier: 2, Revision: 1}
	if _, _, err := service.Update(ctx, "admin", "entry-1", input, "narrow-range"); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("invalidated port binding: %v", err)
	}
	input.PortStart = 12000
	input.AllowDirect = false
	if _, _, err := service.Update(ctx, "admin", "entry-1", input, "deny-direct"); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("invalidated direct permission: %v", err)
	}
	input.AllowDirect = true
	updated, _, err := service.Update(ctx, "admin", "entry-1", input, "network-update")
	if err != nil || updated.Revision != 2 {
		t.Fatalf("network update: %v", err)
	}
	if _, replayed, err := service.Update(ctx, "admin", "entry-1", input, "network-update"); err != nil || !replayed {
		t.Fatalf("network replay: %v", err)
	}
	if _, _, err := service.Update(ctx, "admin", "entry-1", input, "stale-network"); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("network revision: %v", err)
	}
	updated.AllowedExitGroupIDs[0] = "tampered"
	stored, _ := service.Get(ctx, "entry-1")
	if stored.AllowedExitGroupIDs[0] != "exit-1" {
		t.Fatal("network slice escaped store lock")
	}
	if len(s.AuditEvents()) != 2 {
		t.Fatal("rejected mutations appended audit")
	}
}

func TestExitGroupRejectsHiddenEntryNetworkFields(t *testing.T) {
	s, _ := forwardingRepositoryFixture(t)
	service := groupconfig.NewService(s, nil)
	input := groupconfig.Request{
		ConnectHost:       "hidden-entry.example.test",
		PortStart:         12000,
		PortEnd:           12010,
		DirectPolicy:      groupconfig.DirectPolicyDisabled,
		TrafficMultiplier: 1,
		Revision:          1,
	}
	if _, _, err := service.Update(context.Background(), "admin", "exit-1", input, "exit-hidden-entry-fields"); !errors.Is(err, faults.ErrValidation) {
		t.Fatalf("EXIT group accepted entry connection fields: %v", err)
	}
	if network, err := service.Get(context.Background(), "exit-1"); err != nil || network.Revision != 1 {
		t.Fatalf("rejected EXIT policy changed the existing network: %+v, %v", network, err)
	}
}
