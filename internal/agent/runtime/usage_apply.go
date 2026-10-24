package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	usageagent "github.com/hongle/hl-panel/internal/agent/usage"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

var errUsageBeforeApply = errors.New("usage checkpoint must succeed before applying configuration")

func (a *Agent) usageGeneration() (agentv1.NodeConfigGeneration, error) {
	desired, exists, err := a.state.LastKnownGood()
	if err != nil || !exists {
		return 0, err
	}
	var bundle agentv1.ConfigurationBundle
	if err := json.Unmarshal(desired.Config, &bundle); err != nil {
		return 0, err
	}
	for _, fragment := range bundle.Fragments {
		if fragment.Usage != nil {
			return desired.Generation, nil
		}
	}
	// Old bundles remain on the strict current-configuration compatibility
	// path until the capability heartbeat compiles a metadata-bearing bundle.
	return 0, nil
}

func (a *Agent) flushUsage(ctx context.Context) error {
	// Delivery may wait on a remote control-plane response. Keep it outside the
	// engine lifecycle lock so a slow or unavailable panel cannot delay config
	// apply, local recovery, stop, or restart. Reporter serializes its journal
	// transitions and preserves pending payloads across the delivery retry.
	// Collection still shares that lock: the generation, process epoch and
	// counters must describe the same engine on both sides of a config switch.
	a.usageApplyMu.Lock()
	err := a.usageReporter.CollectIfEmpty(ctx)
	a.usageApplyMu.Unlock()
	if err != nil {
		return err
	}
	return a.usageReporter.DeliverPending(ctx)
}

func (a *Agent) applyWithUsage(ctx context.Context, desired agentv1.DesiredNodeConfig) error {
	a.usageApplyMu.Lock()
	defer a.usageApplyMu.Unlock()
	stopped, err := a.state.EngineStopped()
	if err != nil {
		return err
	}
	if stopped {
		return errEngineStopped
	}
	if a.usageReporter != nil {
		current, err := a.state.Load()
		if err != nil {
			return err
		}
		if current.Applied != nil && current.Applied.Generation != desired.Generation {
			// Persist independently of network delivery. An unavailable
			// counter endpoint is logged; it must not prevent engine repair.
			if err := a.usageReporter.CollectOnce(ctx); err != nil {
				if !errors.Is(err, usageagent.ErrCounterReadFailed) {
					return fmt.Errorf("%w: %w", errUsageBeforeApply, err)
				}
				a.logger.Warn("final usage collection before configuration change failed", "error", err)
			}
		}
	}
	if a.trafficControl != nil {
		return a.trafficControl.WithBundle(ctx, desired, func() error {
			return a.reconciler.Apply(ctx, desired)
		})
	}
	return a.reconciler.Apply(ctx, desired)
}
