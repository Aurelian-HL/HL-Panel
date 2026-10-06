package memoryrepo

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groups"
)

func TestSnapshotRejectsSharedMachineForwardingListenerConflicts(t *testing.T) {
	s, request, otherID := sharedEntryGroupsFixture(t)
	node := s.nodes["shared-node"]
	node.CredentialHash = "shared-node-test-credential-hash"
	s.nodes[node.ID] = node
	s.nodesByCredential[node.CredentialHash] = node.ID
	addSharedMember(t, s, request.EntryGroupID)
	service := forwarding.NewService(s, nil)
	request.ListenPort = 12000
	if _, _, err := service.Create(context.Background(), "admin", request, "snapshot-first-rule"); err != nil {
		t.Fatal(err)
	}
	request.EntryGroupID = otherID
	request.Paused = true
	second, _, err := service.Create(context.Background(), "admin", request, "snapshot-second-rule")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSnapshot(raw); err != nil {
		t.Fatalf("valid disjoint-group baseline must restore: %v", err)
	}
	tests := []struct {
		name         string
		retired      bool
		paused       bool
		protocol     forwarding.Protocol
		wantConflict bool
	}{
		{name: "active shared listener", protocol: forwarding.ProtocolTCP, wantConflict: true},
		{name: "paused shared listener", paused: true, protocol: forwarding.ProtocolTCP, wantConflict: true},
		{name: "retired member", retired: true, protocol: forwarding.ProtocolTCP},
		{name: "different protocol", protocol: forwarding.ProtocolUDP},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var state snapshot
			if err := json.Unmarshal(raw, &state); err != nil {
				t.Fatal(err)
			}
			member := groups.Member{GroupID: otherID, NodeID: node.ID, Weight: 1}
			if test.retired {
				retiredAt := time.Now().UTC()
				member.RetiredAt = &retiredAt
			}
			state.MembersByGroup[otherID][node.ID] = member
			rule := state.ForwardRules[second.ID]
			rule.Paused, rule.Protocol = test.paused, test.protocol
			if test.protocol == forwarding.ProtocolUDP {
				rule.IngressProtocol = forwarding.IngressUDP
			}
			state.ForwardRules[second.ID] = rule
			modified, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := DecodeSnapshot(modified)
			if test.wantConflict {
				if err == nil || !strings.Contains(err.Error(), "forwarding listeners on shared node") || restored != nil {
					t.Fatalf("snapshot must fail closed on shared listener conflict: store=%v error=%v", restored != nil, err)
				}
				return
			}
			if err != nil || restored == nil || len(restored.forwardRules) != 2 {
				t.Fatalf("valid listener namespaces did not restore: %v", err)
			}
		})
	}
}
