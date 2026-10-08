package memoryrepo

func (s *Store) DesiredConfigChanges(nodeID string) <-chan struct{} {
	return s.desiredChanges.ForNode(nodeID)
}

// DesiredGenerationIndex is non-secret metadata used by the PostgreSQL
// wrapper to publish notifications only after a transaction commits.
func (s *Store) DesiredGenerationIndex() map[string]int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]int64, len(s.nodes))
	for id, node := range s.nodes {
		result[id] = node.DesiredGeneration
	}
	return result
}
