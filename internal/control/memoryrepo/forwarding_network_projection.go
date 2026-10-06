package memoryrepo

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/serviceaddress"
)

// These defaults are only used when an ENTRY device group has active members
// but no explicit network policy yet. They make the normal NY-style rule
// workflow usable without requiring a second form, while keeping the policy
// deterministic and bounded. Operators can replace the projection later with
// an explicit group-network configuration.
const (
	defaultForwardingPortStart = 20000
	defaultForwardingPortEnd   = 30000
)

// defaultEntryGroupNetworkLocked derives the minimum safe policy needed to
// allocate a listener for a populated entry group. It never mutates the
// store, so it is also safe for read-only import previews.
func (s *Store) defaultEntryGroupNetworkLocked(groupID string) (groupconfig.GroupNetwork, error) {
	group, found := s.deviceGroups[groupID]
	if !found {
		return groupconfig.GroupNetwork{}, fmt.Errorf("%w: entry group does not exist", faults.ErrValidation)
	}
	if group.Kind != groups.KindEntry {
		return groupconfig.GroupNetwork{}, fmt.Errorf("%w: only ENTRY groups can receive an automatic network policy", faults.ErrValidation)
	}

	members := s.membersByGroup[groupID]
	nodeIDs := make([]string, 0, len(members))
	for nodeID, member := range members {
		if member.RetiredAt == nil {
			nodeIDs = append(nodeIDs, nodeID)
		}
	}
	sort.Strings(nodeIDs)
	for _, nodeID := range nodeIDs {
		member := members[nodeID]
		host := strings.TrimSpace(member.DialHost)
		if host == "" {
			host = strings.TrimSpace(s.nodes[nodeID].Hostname)
		}
		host, err := serviceaddress.NormalizeHost(host)
		if err != nil {
			continue
		}
		return groupconfig.GroupNetwork{
			GroupID:           groupID,
			ConnectHost:       host,
			PortStart:         defaultForwardingPortStart,
			PortEnd:           defaultForwardingPortEnd,
			DirectPolicy:      groupconfig.DirectPolicyOptional,
			AllowDirect:       true,
			TrafficMultiplier: 1,
			Revision:          1,
		}, nil
	}
	return groupconfig.GroupNetwork{}, fmt.Errorf("%w: add an active device with a valid hostname before creating a forwarding rule", faults.ErrValidation)
}

// ensureEntryGroupNetworkLocked persists a derived policy only for a real
// mutation. The bool tells the caller whether it must remove the projection
// if the surrounding rule transaction later fails.
func (s *Store) ensureEntryGroupNetworkLocked(groupID string) (groupconfig.GroupNetwork, bool, error) {
	if network, configured := s.groupNetworks[groupID]; configured {
		return network, false, nil
	}
	network, err := s.defaultEntryGroupNetworkLocked(groupID)
	if err != nil {
		return groupconfig.GroupNetwork{}, false, err
	}
	s.groupNetworks[groupID] = network
	return network, true, nil
}
