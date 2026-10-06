package memoryrepo

import (
	"fmt"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groups"
)

// forwardingListenerTransport models the operating-system bind namespace,
// which is narrower than the customer-facing ingress protocol. Raw NY TCP,
// NY SOCKS5, and VLESS Reality all need a TCP socket; only NY UDP has a
// separate transport namespace. Keeping this distinct from IngressProtocol
// prevents protocol labels from permitting an impossible same-port bind.
func forwardingListenerTransport(protocol forwarding.IngressProtocol) string {
	if protocol == forwarding.IngressUDP {
		return "udp"
	}
	return "tcp"
}

// Logical groups do not create separate operating-system port namespaces.
// Paused rules keep their reservation so that a later resume cannot collide.
func (s *Store) entryGroupsShareListenerLocked(firstID, secondID string) bool {
	if firstID == secondID {
		return true
	}
	first, second := s.membersByGroup[firstID], s.membersByGroup[secondID]
	if len(first) > len(second) {
		first, second = second, first
	}
	for nodeID, member := range first {
		if member.RetiredAt != nil {
			continue
		}
		if other, exists := second[nodeID]; exists && other.RetiredAt == nil {
			return true
		}
	}
	return false
}

// Run before changing membership: groups with previously disjoint machines
// may already reserve the same port. Adding one shared machine must not make
// their configurations impossible to bind, including while rules are paused.
func (s *Store) validateForwardingMemberListenersLocked(member groups.Member) error {
	if member.RetiredAt != nil {
		return nil
	}
	type listener struct {
		transport string
		port      int
	}
	joining := make(map[listener]bool)
	for _, rule := range s.forwardRules {
		if rule.EntryGroupID == member.GroupID {
			joining[listener{forwardingListenerTransport(rule.EffectiveIngressProtocol()), rule.ListenPort}] = true
		}
	}
	if len(joining) == 0 {
		return nil
	}
	for _, rule := range s.forwardRules {
		if rule.EntryGroupID == member.GroupID || !joining[listener{forwardingListenerTransport(rule.EffectiveIngressProtocol()), rule.ListenPort}] {
			continue
		}
		other, exists := s.membersByGroup[rule.EntryGroupID][member.NodeID]
		if exists && other.RetiredAt == nil {
			return fmt.Errorf("%w: adding this machine would conflict with a forwarding listener reserved by another entry group", faults.ErrConflict)
		}
	}
	return nil
}
