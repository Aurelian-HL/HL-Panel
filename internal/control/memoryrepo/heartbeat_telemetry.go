package memoryrepo

import "github.com/hongle/hl-panel/internal/control/nodes"

// MergeHeartbeatTelemetry overlays readings only for an existing, unchanged
// identity. It cannot resurrect deleted nodes or overwrite business state.
func (s *Store) MergeHeartbeatTelemetry(samples []nodes.Node) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sample := range samples {
		node, exists := s.nodes[sample.ID]
		if !exists || node.DeletedAt != nil || node.CredentialHash != sample.CredentialHash || sample.LastHeartbeatAt == nil {
			continue
		}
		if node.LastHeartbeatAt != nil && !sample.LastHeartbeatAt.After(*node.LastHeartbeatAt) {
			continue
		}
		node.Resources = cloneAnyMap(sample.Resources)
		node.LastHeartbeatAt = timePointer(*sample.LastHeartbeatAt)
		if sample.LastHeartbeatAt.After(node.UpdatedAt) {
			node.UpdatedAt = *sample.LastHeartbeatAt
		}
		s.nodes[node.ID] = node
	}
}
