// Package endpointselector adapts the shared endpoint router to the TCP front
// door without coupling either package to the other's internal state.
package endpointselector

import (
	"errors"

	"github.com/hongle/hl-panel/internal/gateway/tcpproxy"
	"github.com/hongle/hl-panel/internal/routing/endpointrouter"
)

var ErrRouterRequired = errors.New("endpoint router is required")

// RouterSelector converts a reserved routing endpoint into a TCP backend. It
// preserves the endpoint ID so the active-connection reservation is released.
type RouterSelector struct {
	router *endpointrouter.Router
}

func NewRouterSelector(router *endpointrouter.Router) (*RouterSelector, error) {
	if router == nil {
		return nil, ErrRouterRequired
	}
	return &RouterSelector{router: router}, nil
}

func (s *RouterSelector) Select() (tcpproxy.Backend, error) {
	return s.SelectKey("")
}

func (s *RouterSelector) SelectKey(key string) (tcpproxy.Backend, error) {
	endpoint, err := s.router.SelectKey(key)
	if err != nil {
		return tcpproxy.Backend{}, err
	}
	return tcpproxy.Backend{ID: endpoint.ID, Address: endpoint.Address}, nil
}

func (s *RouterSelector) Release(endpointID string) error {
	return s.router.Release(endpointID)
}

var _ tcpproxy.Selector = (*RouterSelector)(nil)
var _ tcpproxy.KeyedSelector = (*RouterSelector)(nil)
