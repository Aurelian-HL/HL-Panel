package memoryrepo

import (
	"fmt"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/serviceaddress"
)

// An EXIT_GROUP rule is still pending engine support. This check ensures its
// saved route at least names one active, addressable exit member; it does not
// claim the missing inter-node transport has been provisioned.
func (s *Store) validateExitGroupMembersLocked(groupID string) error {
	for nodeID, member := range s.membersByGroup[groupID] {
		if member.RetiredAt != nil || member.DialHost == "" {
			continue
		}
		if _, exists := s.nodes[nodeID]; !exists {
			continue
		}
		if _, err := serviceaddress.NormalizeHost(member.DialHost); err == nil {
			return nil
		}
	}
	return fmt.Errorf("%w: exit group requires an active node with a dial host", faults.ErrValidation)
}
