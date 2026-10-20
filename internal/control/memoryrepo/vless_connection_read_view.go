package memoryrepo

import (
	"context"

	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/vlessconnection"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
)

// The resolver stays inside the detached view: credential material never
// becomes a public read-view API, and readiness is evaluated for every call.
func (v *ForwardingReadView) connectionResolver() (*vlessconnection.Service, error) {
	identity, err := vlessidentity.NewService(v.state, nil, nil)
	if err != nil {
		return nil, err
	}
	return vlessconnection.NewService(identity, forwarding.NewService(v.state, nil),
		endpoints.NewServiceWithReadyCandidateSource(v.state, nil, gatewaymembership.NewService(v.state))), nil
}

func (v *ForwardingReadView) ResolveForwardingConnection(ctx context.Context, adminID, ruleID string) (vlessconnection.Connection, error) {
	resolver, err := v.connectionResolver()
	if err != nil {
		return vlessconnection.Connection{}, err
	}
	return resolver.ResolveForwardingConnection(ctx, adminID, ruleID)
}

func (v *ForwardingReadView) ResolveIdentityConnection(ctx context.Context, adminID, bindingID string) (vlessconnection.Connection, error) {
	resolver, err := v.connectionResolver()
	if err != nil {
		return vlessconnection.Connection{}, err
	}
	return resolver.ResolveIdentityConnection(ctx, adminID, bindingID)
}

func (v *ForwardingReadView) ResolveSubscriptionLines(ctx context.Context, adminID, customerID string, bindings []string) (map[string]string, map[string]error, error) {
	resolver, err := v.connectionResolver()
	if err != nil {
		return nil, nil, err
	}
	return resolver.ResolveSubscriptionLines(ctx, adminID, customerID, bindings)
}
