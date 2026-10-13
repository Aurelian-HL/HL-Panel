package memoryrepo

import "github.com/hongle/hl-panel/internal/control/gatewaymembership"

// MergeProtocolHealthOverlays restores already validated, committed proofs.
// The persistence adapter serializes these with business writes and folds them
// into the base snapshot before editing configuration. This is restoration,
// not a new challenge: an expired lease remains expired and is never extended.
func (s *Store) MergeProtocolHealthOverlays(observations []gatewaymembership.ProtocolObservation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, observation := range observations {
		poolID, err := s.endpointPoolIDForRuleLocked(observation.RuleID)
		if err != nil {
			continue
		}
		member, exists := s.endpointMembers[poolID][observation.NodeID]
		if !exists {
			continue
		}
		if previous, exists := s.protocolHealth[poolID][observation.NodeID]; exists && !observation.CanReplace(previous) {
			continue
		}
		if s.protocolHealth[poolID] == nil {
			s.protocolHealth[poolID] = make(map[string]gatewaymembership.ProtocolObservation)
		}
		s.protocolHealth[poolID][observation.NodeID] = observation
		at := observation.VerifiedAt.UTC()
		member.LastHealthAt, member.LastHealthReason, member.UpdatedAt = &at, "protocol_probe", at
		s.endpointMembers[poolID][observation.NodeID] = member
	}
}
