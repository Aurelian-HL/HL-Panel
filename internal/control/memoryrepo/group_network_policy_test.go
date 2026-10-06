package memoryrepo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
)

func TestForwardingAllocatesOnlyInsideDisjointPortRanges(t *testing.T) {
	s, request := forwardingRepositoryFixture(t)
	network := s.groupNetworks[request.EntryGroupID]
	network.PortStart, network.PortEnd = 12000, 12002
	network.PortRanges = []groupconfig.PortRange{{Start: 12000, End: 12000}, {Start: 12002, End: 12002}}
	s.groupNetworks[network.GroupID] = network
	service := forwarding.NewService(s, nil)

	first, _, err := service.Create(context.Background(), "admin", request, "disjoint-first")
	if err != nil {
		t.Fatal(err)
	}
	request.Name = "second"
	second, _, err := service.Create(context.Background(), "admin", request, "disjoint-second")
	if err != nil {
		t.Fatal(err)
	}
	if first.ListenPort != 12000 || second.ListenPort != 12002 {
		t.Fatalf("allocator entered a port-range gap: %d, %d", first.ListenPort, second.ListenPort)
	}
	request.Name = "exhausted"
	if _, _, err := service.Create(context.Background(), "admin", request, "disjoint-exhausted"); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("expected disjoint range exhaustion, got %v", err)
	}
}

func TestForwardingEnforcesEntryAndExitNetworkAuthorization(t *testing.T) {
	s, request := forwardingRepositoryFixture(t)
	entryNetwork := s.groupNetworks[request.EntryGroupID]
	entryNetwork.AllowedUserGroupIDs = []string{"other-users"}
	s.groupNetworks[entryNetwork.GroupID] = entryNetwork
	service := forwarding.NewService(s, nil)
	if _, _, err := service.Create(context.Background(), "admin", request, "entry-user-denied"); !errors.Is(err, faults.ErrValidation) {
		t.Fatalf("entry user-group restriction was not enforced: %v", err)
	}

	entryNetwork.AllowedUserGroupIDs = []string{"ugrp-1"}
	s.groupNetworks[entryNetwork.GroupID] = entryNetwork
	request.EgressMode = forwarding.EgressExitGroup
	request.ExitGroupID = "exit-1"
	s.groupNetworks["exit-1"] = groupconfig.GroupNetwork{
		GroupID: "exit-1", DirectPolicy: groupconfig.DirectPolicyDisabled,
		AllowedEntryGroupIDs: []string{"other-entry"}, AllowedUserGroupIDs: []string{"ugrp-1"},
		TrafficMultiplier: 1, Revision: 1,
	}
	if _, _, err := service.Create(context.Background(), "admin", request, "exit-entry-denied"); !errors.Is(err, faults.ErrValidation) {
		t.Fatalf("exit reverse-entry restriction was not enforced: %v", err)
	}

	exitNetwork := s.groupNetworks["exit-1"]
	exitNetwork.AllowedEntryGroupIDs = []string{"entry-1"}
	exitNetwork.AllowedUserGroupIDs = []string{"other-users"}
	s.groupNetworks[exitNetwork.GroupID] = exitNetwork
	if _, _, err := service.Create(context.Background(), "admin", request, "exit-user-denied"); !errors.Is(err, faults.ErrValidation) {
		t.Fatalf("exit user-group restriction was not enforced: %v", err)
	}

	exitNetwork.AllowedUserGroupIDs = []string{"ugrp-1"}
	s.groupNetworks[exitNetwork.GroupID] = exitNetwork
	if _, _, err := service.Create(context.Background(), "admin", request, "exit-authorized"); err != nil {
		t.Fatalf("fully authorized exit route was rejected: %v", err)
	}
}

func TestExitGroupRuleRequiresConfiguredAddressableExit(t *testing.T) {
	newRule := func(t *testing.T) (*Store, forwarding.Request) {
		t.Helper()
		store, request := forwardingRepositoryFixture(t)
		request.EgressMode = forwarding.EgressExitGroup
		request.ExitGroupID = "exit-1"
		return store, request
	}
	tests := []struct {
		name string
		edit func(*Store)
	}{
		{"missing network", func(s *Store) { delete(s.groupNetworks, "exit-1") }},
		{"unconfigured network", func(s *Store) {
			network := s.groupNetworks["exit-1"]
			network.Revision = 0
			s.groupNetworks["exit-1"] = network
		}},
		{"missing dial host", func(s *Store) {
			member := s.membersByGroup["exit-1"]["exit-node"]
			member.DialHost = ""
			s.membersByGroup["exit-1"]["exit-node"] = member
		}},
		{"missing node", func(s *Store) { delete(s.nodes, "exit-node") }},
		{"retired node", func(s *Store) {
			member := s.membersByGroup["exit-1"]["exit-node"]
			at := time.Now().UTC()
			member.RetiredAt = &at
			s.membersByGroup["exit-1"]["exit-node"] = member
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, request := newRule(t)
			tt.edit(store)
			if _, _, err := forwarding.NewService(store, nil).Create(context.Background(), "admin", request, tt.name); !errors.Is(err, faults.ErrValidation) {
				t.Fatalf("invalid EXIT_GROUP topology accepted: %v", err)
			}
		})
	}
}

func TestGroupNetworkRejectsMissingReferencesAndInvalidFallback(t *testing.T) {
	s, _ := forwardingRepositoryFixture(t)
	service := groupconfig.NewService(s, nil)
	ctx := context.Background()

	request := groupconfig.Request{
		ConnectHost: "entry.example.test", PortRanges: []groupconfig.PortRange{{Start: 12000, End: 12010}},
		AllowDirect: true, AllowedUserGroupIDs: []string{"missing-users"}, TrafficMultiplier: 1, Revision: 1,
	}
	if _, _, err := service.Update(ctx, "admin", "entry-1", request, "missing-users"); !errors.Is(err, faults.ErrValidation) {
		t.Fatalf("missing user group was accepted: %v", err)
	}

	request.AllowedUserGroupIDs = []string{"ugrp-1"}
	request.AllowedExitGroupIDs = []string{"exit-1"}
	request.FallbackExitGroupID = "missing-exit"
	if _, _, err := service.Update(ctx, "admin", "entry-1", request, "missing-fallback"); !errors.Is(err, faults.ErrValidation) {
		t.Fatalf("fallback outside allowed exits was accepted: %v", err)
	}
}
