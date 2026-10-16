package postgressnapshot

import (
	"context"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

func (s *Store) MarkActivated(ctx context.Context, ruleID string, expectedRevision int64, event audit.Event) error {
	return mutate(ctx, s, func(state *memoryrepo.Store) error { return state.MarkActivated(ctx, ruleID, expectedRevision, event) })
}

func (s *Store) GatewayMembershipState(ctx context.Context, poolID string) (gatewaymembership.State, error) {
	return readForwarding(ctx, s, func(state *memoryrepo.ForwardingReadView) (gatewaymembership.State, error) {
		return state.GatewayMembershipState(ctx, poolID)
	})
}

func (s *Store) GatewayMembershipStates(ctx context.Context, poolIDs []string) ([]gatewaymembership.State, error) {
	return readForwarding(ctx, s, func(state *memoryrepo.ForwardingReadView) ([]gatewaymembership.State, error) {
		return state.GatewayMembershipStates(ctx, poolIDs)
	})
}

func (s *Store) PublishProtocolHealth(ctx context.Context, observation gatewaymembership.ProtocolObservation) error {
	results, err := s.publishProtocolHealth(ctx, []gatewaymembership.ProtocolObservation{observation}, false)
	if err != nil {
		return err
	}
	return results[0]
}

func (s *Store) ProtocolProbeConfig(ctx context.Context, ruleID string) (gatewaymembership.ProtocolProbeConfig, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) (gatewaymembership.ProtocolProbeConfig, error) {
		return state.ProtocolProbeConfig(ctx, ruleID)
	})
}

func (s *Store) ConfigureProtocolProbe(ctx context.Context, ruleID string, config gatewaymembership.ProtocolProbeConfig) error {
	return mutate(ctx, s, func(state *memoryrepo.Store) error {
		return state.ConfigureProtocolProbe(ctx, ruleID, config)
	})
}

func (s *Store) ConfigureProtocolProbes(ctx context.Context, configs map[string]gatewaymembership.ProtocolProbeConfig) error {
	return mutate(ctx, s, func(state *memoryrepo.Store) error { return state.ConfigureProtocolProbes(ctx, configs) })
}

func (s *Store) ProtocolProbeStates(ctx context.Context) ([]gatewaymembership.ProbeState, error) {
	return readForwarding(ctx, s, func(state *memoryrepo.ForwardingReadView) ([]gatewaymembership.ProbeState, error) {
		return state.ProtocolProbeStates(ctx)
	})
}

func (s *Store) PublishProtocolHealthBatch(ctx context.Context, observations []gatewaymembership.ProtocolObservation) ([]error, error) {
	return s.publishProtocolHealth(ctx, observations, true)
}
