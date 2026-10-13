package generations

import "sync"

// ChangeSignals wakes authenticated node configuration waits. It carries no
// configuration or credential material; callers always reread durable state.
// The zero value is ready to use. Notifications may coalesce without losing a
// generation because subscribers register before reading the current state.
type ChangeSignals struct {
	mu    sync.Mutex
	nodes map[string]signalSubscription
}

type signalSubscription struct {
	channel chan struct{}
	refs    int
}

func (s *ChangeSignals) ForNode(nodeID string) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nodes == nil {
		s.nodes = make(map[string]signalSubscription)
	}
	subscription := s.nodes[nodeID]
	if subscription.channel == nil {
		subscription.channel = make(chan struct{})
	}
	subscription.refs++
	s.nodes[nodeID] = subscription
	return subscription.channel
}

// Release drops one waiter's reference to a node signal. A notification can
// remove the subscription first, so releasing an already notified channel is
// intentionally a no-op. The channel identity check prevents an old waiter
// from deleting a newer subscription registered after Notify.
func (s *ChangeSignals) Release(nodeID string, channel <-chan struct{}) {
	if channel == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	subscription, ok := s.nodes[nodeID]
	if !ok || subscription.channel != channel {
		return
	}
	if subscription.refs <= 1 {
		delete(s.nodes, nodeID)
		return
	}
	subscription.refs--
	s.nodes[nodeID] = subscription
}

func (s *ChangeSignals) Notify(nodeID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if subscription, ok := s.nodes[nodeID]; ok && subscription.channel != nil {
		close(subscription.channel)
		delete(s.nodes, nodeID)
	}
}

type ConfigChangeSource interface {
	DesiredConfigChanges(string) <-chan struct{}
}

type ConfigChangeReleaser interface {
	ReleaseDesiredConfigChanges(string, <-chan struct{})
}

func (s *Service) DesiredConfigChanges(nodeID string) <-chan struct{} {
	if source, ok := s.repository.(ConfigChangeSource); ok {
		return source.DesiredConfigChanges(nodeID)
	}
	return nil
}

func (s *Service) ReleaseDesiredConfigChanges(nodeID string, channel <-chan struct{}) {
	if source, ok := s.repository.(ConfigChangeReleaser); ok {
		source.ReleaseDesiredConfigChanges(nodeID, channel)
	}
}
