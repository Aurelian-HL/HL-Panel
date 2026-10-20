package postgressnapshot

import (
	"context"

	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/vlessconnection"
)

var _ vlessconnection.ConnectionSource = (*Store)(nil)

// Resolve both connection routes in one validated snapshot transaction. The
// request view merges current health/usage/heartbeat inputs; no ready result
// or credential escapes into a result cache.
func (s *Store) ResolveForwardingConnection(ctx context.Context, adminID, ruleID string) (vlessconnection.Connection, error) {
	return readForwarding(ctx, s, func(view *memoryrepo.ForwardingReadView) (vlessconnection.Connection, error) {
		return view.ResolveForwardingConnection(ctx, adminID, ruleID)
	})
}

func (s *Store) ResolveIdentityConnection(ctx context.Context, adminID, bindingID string) (vlessconnection.Connection, error) {
	return readForwarding(ctx, s, func(view *memoryrepo.ForwardingReadView) (vlessconnection.Connection, error) {
		return view.ResolveIdentityConnection(ctx, adminID, bindingID)
	})
}
