package healthprobe

import (
	"context"
	"errors"
	"sync"

	"github.com/hongle/hl-panel/internal/routing/endpointrouter"
)

var ErrRouterRequired = errors.New("reachability controller router is required")

// Controller keeps authoritative membership replacement, probe-target
// replacement, probe state transitions, and router veto updates linearizable.
// Callers must route every membership replacement through the Controller once
// it has been enabled.
type Controller struct {
	mu      sync.Mutex
	router  *endpointrouter.Router
	monitor *Monitor
}

func NewController(router *endpointrouter.Router, config Config, observeResult ResultObserver) (*Controller, error) {
	if router == nil {
		return nil, ErrRouterRequired
	}
	controller := &Controller{router: router}
	config.Observe = nil
	config.apply = controller.applyVerdictLocked
	config.notify = observeResult
	monitor, err := New(config)
	if err != nil {
		return nil, err
	}
	monitor.transition = &controller.mu
	controller.monitor = monitor
	return controller, nil
}

func (c *Controller) ReplaceVersionedMembership(revision uint64, endpoints []endpointrouter.Endpoint) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.router.ReplaceVersionedMembership(revision, endpoints); err != nil {
		return err
	}
	targets := make([]Target, 0, len(endpoints))
	for _, endpoint := range endpoints {
		targets = append(targets, Target{
			ID: endpoint.ID, Address: endpoint.Address, MembershipRevision: revision,
			AuthoritativelyReady: endpoint.Status == endpointrouter.EndpointReady,
			LeaseExpiresAt:       endpoint.HealthLeaseExpiresAt,
		})
	}
	c.monitor.ReplaceTargets(targets)
	return nil
}

func (c *Controller) Run(ctx context.Context) {
	c.monitor.Run(ctx)
}

// applyVerdictLocked is invoked by Monitor while c.mu is held through the
// monitor's transition lock. Do not call it directly.
func (c *Controller) applyVerdictLocked(verdict Verdict) error {
	return c.router.SetLocalReachabilityAtRevision(
		verdict.ID, verdict.Address, verdict.MembershipRevision, verdict.Reachable,
	)
}
