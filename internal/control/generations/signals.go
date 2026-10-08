package generations

import "sync"

// ChangeSignals wakes authenticated node configuration waits. It carries no
// configuration or credential material; callers always reread durable state.
// The zero value is ready to use. Notifications may coalesce without losing a
// generation because subscribers register before reading the current state.
type ChangeSignals struct {
	mu    sync.Mutex
	nodes map[string]chan struct{}
}

func (s *ChangeSignals) ForNode(nodeID string) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nodes == nil {
		s.nodes = make(map[string]chan struct{})
	}
	if s.nodes[nodeID] == nil {
		s.nodes[nodeID] = make(chan struct{})
	}
	return s.nodes[nodeID]
}

func (s *ChangeSignals) Notify(nodeID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if channel := s.nodes[nodeID]; channel != nil {
		close(channel)
		delete(s.nodes, nodeID)
	}
}

type ConfigChangeSource interface {
	DesiredConfigChanges(string) <-chan struct{}
}

func (s *Service) DesiredConfigChanges(nodeID string) <-chan struct{} {
	if source, ok := s.repository.(ConfigChangeSource); ok {
		return source.DesiredConfigChanges(nodeID)
	}
	return nil
}
