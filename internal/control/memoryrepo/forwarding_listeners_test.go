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
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

func sharedEntryGroupsFixture(t *testing.T) (*Store, forwarding.Request, string) {
	t.Helper()
	s, request := forwardingRepositoryFixture(t)
	other, err := groups.NewService(s, nil).Create(context.Background(), "admin", "second entry", groups.KindEntry, "weighted_round_robin", "")
	if err != nil {
		t.Fatal(err)
	}
	network := s.groupNetworks[request.EntryGroupID]
	network.GroupID = other.ID
	s.groupNetworks[other.ID] = network
	group := s.userGroups["ugrp-1"]
	group.AllowedEntryGroupIDs = append(group.AllowedEntryGroupIDs, other.ID)
	s.userGroups[group.ID] = group
	s.nodes["shared-node"] = nodes.Node{ID: "shared-node"}
	return s, request, other.ID
}

func addSharedMember(t *testing.T, s *Store, groupID string) {
	t.Helper()
	if _, _, err := groups.NewService(s, nil).AddMember(context.Background(), "admin", groupID, "shared-node", 1, 0); err != nil {
		t.Fatal(err)
	}
}

func TestSharedMachineRejectsDuplicatePortsAcrossGroupsAndReservesPausedRules(t *testing.T) {
	s, request, otherID := sharedEntryGroupsFixture(t)
	addSharedMember(t, s, request.EntryGroupID)
	addSharedMember(t, s, otherID)
	service := forwarding.NewService(s, nil)
	request.ListenPort = 12000
	request.Paused = true
	if _, _, err := service.Create(context.Background(), "admin", request, "paused-reservation"); err != nil {
		t.Fatal(err)
	}
	request.EntryGroupID = otherID
	request.Paused = false
	auditsBefore := len(s.AuditEvents())
	if _, _, err := service.Create(context.Background(), "admin", request, "shared-port-conflict"); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("cross-group same-node listener accepted: %v", err)
	}
	if len(s.forwardRules) != 1 || len(s.AuditEvents()) != auditsBefore {
		t.Fatal("rejected rule partially committed")
	}
	request.ListenPort = 0
	automatic, _, err := service.Create(context.Background(), "admin", request, "shared-automatic-port")
	if err != nil || automatic.ListenPort != 12001 {
		t.Fatalf("automatic allocation failed to avoid shared machine reservation: %+v %v", automatic, err)
	}
	request.Protocol = forwarding.ProtocolUDP
	request.ListenPort = 12000
	if _, _, err := service.Create(context.Background(), "admin", request, "udp-separate-namespace"); err != nil {
		t.Fatalf("independent UDP port was rejected: %v", err)
	}
}

func TestTCPListenerNamespaceRejectsSOCKS5AndVLESSSamePort(t *testing.T) {
	s, request := forwardingRepositoryFixture(t)
	service := forwarding.NewService(s, nil)
	request.ListenPort = 12000
	if _, _, err := service.Create(context.Background(), "admin", request, "tcp-port"); err != nil {
		t.Fatal(err)
	}
	for name, ingress := range map[string]forwarding.IngressProtocol{
		"socks5": forwarding.IngressSOCKS5,
		"vless":  forwarding.IngressVLESSReality,
	} {
		t.Run(name, func(t *testing.T) {
			candidate := request
			candidate.IngressProtocol = ingress
			candidate.Name = name + " same port"
			candidate.ListenPort = 12000
			if ingress == forwarding.IngressVLESSReality {
				candidate.VLESSOutboundMode = forwarding.VLESSOutboundSOCKS5
				candidate.VLESSSOCKS5Host = "landing.example.test"
				candidate.VLESSSOCKS5Port = 1080
				candidate.VLESSSOCKS5Username = "landing-user"
				candidate.VLESSSOCKS5Password = "landing-secret"
				candidate.RealityServerName = "www.example.com"
				candidate.RealityPublicKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
				candidate.RealityShortID = "0123456789abcdef"
			}
			if _, _, err := service.Create(context.Background(), "admin", candidate, name+"-same-port"); !errors.Is(err, faults.ErrConflict) {
				t.Fatalf("same TCP listener port was accepted for %s: %v", name, err)
			}
		})
	}
	udp := request
	udp.Name = "udp same port"
	udp.Protocol = forwarding.ProtocolUDP
	udp.IngressProtocol = forwarding.IngressUDP
	udp.ListenPort = 12000
	if _, _, err := service.Create(context.Background(), "admin", udp, "udp-same-port"); err != nil {
		t.Fatalf("TCP and UDP should retain independent socket namespaces: %v", err)
	}
}

func TestAddingMemberCannotMergeConflictingListenerNamespaces(t *testing.T) {
	s, request, otherID := sharedEntryGroupsFixture(t)
	addSharedMember(t, s, request.EntryGroupID)
	service := forwarding.NewService(s, nil)
	request.ListenPort = 12000
	if _, _, err := service.Create(context.Background(), "admin", request, "disjoint-first"); err != nil {
		t.Fatal(err)
	}
	request.EntryGroupID = otherID
	request.Paused = true
	if _, _, err := service.Create(context.Background(), "admin", request, "disjoint-second"); err != nil {
		t.Fatalf("disjoint groups should allow a repeated port: %v", err)
	}
	auditsBefore := len(s.AuditEvents())
	generationBefore := s.nodes["shared-node"].DesiredGeneration
	_, _, err := groups.NewService(s, nil).AddMember(context.Background(), "admin", otherID, "shared-node", 1, 0)
	if !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("adding a shared machine accepted conflicting rules: %v", err)
	}
	if _, found := s.membersByGroup[otherID]["shared-node"]; found {
		t.Fatal("failed add changed membership")
	}
	if len(s.AuditEvents()) != auditsBefore || s.nodes["shared-node"].DesiredGeneration != generationBefore {
		t.Fatal("failed add changed audit or node generation")
	}
}

func TestRetiredMembersDoNotReserveAnotherGroupPortButReactivationIsChecked(t *testing.T) {
	s, request, otherID := sharedEntryGroupsFixture(t)
	addSharedMember(t, s, request.EntryGroupID)
	retiredAt := time.Now()
	s.membersByGroup[otherID]["shared-node"] = groups.Member{GroupID: otherID, NodeID: "shared-node", Weight: 1, RetiredAt: &retiredAt}
	service := forwarding.NewService(s, nil)
	request.ListenPort = 12000
	if _, _, err := service.Create(context.Background(), "admin", request, "active-group-rule"); err != nil {
		t.Fatal(err)
	}
	request.EntryGroupID = otherID
	if _, _, err := service.Create(context.Background(), "admin", request, "retired-member-group-rule"); err != nil {
		t.Fatalf("retired overlap incorrectly blocked a rule: %v", err)
	}
	_, _, err := groups.NewService(s, nil).AddMember(context.Background(), "admin", otherID, "shared-node", 1, 0)
	if !errors.Is(err, faults.ErrConflict) {
		t.Fatal("reactivation bypassed listener collision detection")
	}
	if s.membersByGroup[otherID]["shared-node"].RetiredAt == nil {
		t.Fatal("failed reactivation cleared retired state")
	}
}

func TestConcurrentSharedGroupsAllocateUniqueMachinePorts(t *testing.T) {
	s, request, otherID := sharedEntryGroupsFixture(t)
	addSharedMember(t, s, request.EntryGroupID)
	addSharedMember(t, s, otherID)
	service := forwarding.NewService(s, nil)
	type result struct {
		rule forwarding.Rule
		err  error
	}
	results := make(chan result, 10)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i := 0; i < 10; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			input := request
			if i%2 != 0 {
				input.EntryGroupID = otherID
			}
			<-start
			rule, _, err := service.Create(context.Background(), "admin", input, fmt.Sprintf("shared-concurrent-%d", i))
			results <- result{rule, err}
		}(i)
	}
	close(start)
	workers.Wait()
	close(results)
	ports := make(map[int]bool)
	for got := range results {
		if got.err != nil {
			t.Fatal(got.err)
		}
		if ports[got.rule.ListenPort] {
			t.Fatalf("shared machine port %d allocated twice", got.rule.ListenPort)
		}
		ports[got.rule.ListenPort] = true
	}
	if len(ports) != 10 {
		t.Fatalf("allocated %d distinct ports, want 10", len(ports))
	}
	if _, _, err := service.Create(context.Background(), "admin", request, "shared-range-exhausted"); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("shared listener namespace was not exhausted: %v", err)
	}
}

func TestConcurrentMembershipAndRuleCreationCannotCommitConflictingListeners(t *testing.T) {
	for attempt := 0; attempt < 12; attempt++ {
		t.Run(fmt.Sprintf("attempt-%d", attempt), func(t *testing.T) {
			s, request, otherID := sharedEntryGroupsFixture(t)
			addSharedMember(t, s, request.EntryGroupID)
			service := forwarding.NewService(s, nil)
			request.ListenPort = 12000
			if _, _, err := service.Create(context.Background(), "admin", request, "before-membership-race"); err != nil {
				t.Fatal(err)
			}
			request.EntryGroupID = otherID
			auditsBefore := len(s.AuditEvents())
			start := make(chan struct{})
			results := make(chan error, 2)
			go func() {
				<-start
				_, _, err := service.Create(context.Background(), "admin", request, "racing-rule")
				results <- err
			}()
			go func() {
				<-start
				_, _, err := groups.NewService(s, nil).AddMember(context.Background(), "admin", otherID, "shared-node", 1, 0)
				results <- err
			}()
			close(start)
			first, second := <-results, <-results
			if !((first == nil && errors.Is(second, faults.ErrConflict)) || (second == nil && errors.Is(first, faults.ErrConflict))) {
				t.Fatalf("expected one commit and one conflict, got %v and %v", first, second)
			}
			if len(s.AuditEvents()) != auditsBefore+1 {
				t.Fatal("rejected concurrent mutation appended audit")
			}
			if s.entryGroupsShareListenerLocked(request.EntryGroupID, "entry-1") && len(s.forwardRules) != 1 {
				t.Fatal("concurrent mutations committed overlapping machine listeners")
			}
		})
	}
}

func TestSharedMachineConflictOnRuleEditPreservesOriginalReservation(t *testing.T) {
	s, request, otherID := sharedEntryGroupsFixture(t)
	addSharedMember(t, s, request.EntryGroupID)
	addSharedMember(t, s, otherID)
	service := forwarding.NewService(s, nil)
	request.ListenPort = 12000
	if _, _, err := service.Create(context.Background(), "admin", request, "reserved-port"); err != nil {
		t.Fatal(err)
	}
	request.EntryGroupID = otherID
	request.ListenPort = 12001
	rule, _, err := service.Create(context.Background(), "admin", request, "editable-port")
	if err != nil {
		t.Fatal(err)
	}
	request.Revision = rule.Revision
	request.ListenPort = 12000
	request.Paused = true
	auditsBefore := len(s.AuditEvents())
	if _, _, err := service.Update(context.Background(), "admin", rule.ID, request, "conflicting-edit"); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("editing paused rule accepted shared machine port: %v", err)
	}
	stored, err := service.Get(context.Background(), rule.ID)
	if err != nil || stored.ListenPort != 12001 || stored.Paused || stored.Revision != rule.Revision {
		t.Fatalf("rejected edit changed the saved rule: %+v %v", stored, err)
	}
	if len(s.AuditEvents()) != auditsBefore {
		t.Fatal("rejected edit appended audit")
	}
}
