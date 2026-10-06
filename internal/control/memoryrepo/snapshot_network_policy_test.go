package memoryrepo

import (
	"testing"

	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/groups"
)

func networkPolicySnapshotFixture(t *testing.T) *Store {
	t.Helper()
	s, customer := customerRepositoryFixture(t)
	s.customers[customer.ID] = customer
	s.groupNetworks["entry-1"] = groupconfig.GroupNetwork{
		GroupID: "entry-1", ConnectHost: "entry.example.test", PortStart: 12000, PortEnd: 12002,
		PortRanges:   []groupconfig.PortRange{{Start: 12000, End: 12000}, {Start: 12002, End: 12002}},
		DirectPolicy: groupconfig.DirectPolicyOptional, AllowDirect: true,
		AllowedUserGroupIDs: []string{"ugrp-1"}, AllowedExitGroupIDs: []string{"exit-1"}, FallbackExitGroupID: "exit-1",
		TrafficMultiplier: 1, Revision: 1,
	}
	s.groupNetworks["exit-1"] = groupconfig.GroupNetwork{
		GroupID: "exit-1", DirectPolicy: groupconfig.DirectPolicyDisabled,
		AllowedUserGroupIDs: []string{"ugrp-1"}, AllowedEntryGroupIDs: []string{"entry-1"},
		TrafficMultiplier: 1, Revision: 1,
	}
	s.forwardRules["rule-1"] = forwarding.Rule{
		ID: "rule-1", Name: "snapshot policy", CustomerID: customer.ID,
		EntryGroupID: "entry-1", ExitGroupID: "exit-1", EgressMode: forwarding.EgressExitGroup,
		Protocol: forwarding.ProtocolTCP, ListenPort: 12002,
		Targets: []forwarding.Target{{Host: "127.0.0.1", Port: 1080}}, SelectionPolicy: forwarding.SelectionRoundRobin,
		Paused: true, Revision: 1,
	}
	return s
}

func TestSnapshotPreservesAndValidatesAdvancedNetworkPolicy(t *testing.T) {
	s := networkPolicySnapshotFixture(t)
	raw, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	entry := restored.groupNetworks["entry-1"]
	exit := restored.groupNetworks["exit-1"]
	if len(entry.PortRanges) != 2 || entry.FallbackExitGroupID != "exit-1" || len(entry.AllowedUserGroupIDs) != 1 || len(exit.AllowedEntryGroupIDs) != 1 {
		t.Fatalf("advanced network policy was not preserved: entry=%+v exit=%+v", entry, exit)
	}
}

func TestSnapshotRejectsRuleInsidePortRangeGap(t *testing.T) {
	s := networkPolicySnapshotFixture(t)
	rule := s.forwardRules["rule-1"]
	rule.ListenPort = 12001
	s.forwardRules[rule.ID] = rule
	raw, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSnapshot(raw); err == nil {
		t.Fatal("snapshot accepted a rule inside an unallocated port-range gap")
	}
}

func TestSnapshotRejectsBrokenReverseExitAuthorization(t *testing.T) {
	s := networkPolicySnapshotFixture(t)
	exit := s.groupNetworks["exit-1"]
	exit.AllowedEntryGroupIDs = []string{}
	// Empty means unrestricted, so use another valid entry group to create a real deny list.
	other := s.deviceGroups["entry-1"]
	other.ID, other.Name = "entry-2", "other entry"
	s.deviceGroups[other.ID] = other
	s.membersByGroup[other.ID] = map[string]groups.Member{}
	s.revisionByIdempotency[other.ID] = map[string]string{}
	exit.AllowedEntryGroupIDs = []string{other.ID}
	s.groupNetworks[exit.GroupID] = exit
	raw, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSnapshot(raw); err == nil {
		t.Fatal("snapshot accepted a rule denied by the exit reverse-entry policy")
	}
}
