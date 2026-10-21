package postgressnapshot

import (
	"context"

	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/vlessconnection"
)

var _ vlessconnection.ConfigurationSource = (*Store)(nil)

// Configuration reads retain the same authoritative snapshot transaction and
// current availability overlays as the live routes, but grant no live readiness.
func (s *Store) ResolveForwardingConfiguration(ctx context.Context, adminID, ruleID string) (vlessconnection.Configuration, error) {
	return readForwarding(ctx, s, func(view *memoryrepo.ForwardingReadView) (vlessconnection.Configuration, error) {
		return view.ResolveForwardingConfiguration(ctx, adminID, ruleID)
	})
}
