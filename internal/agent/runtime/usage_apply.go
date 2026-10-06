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
	a.usageApplyMu.Lock()
	defer a.usageApplyMu.Unlock()
	return a.usageReporter.FlushOnce(ctx)
}

func (a *Agent) applyWithUsage(ctx context.Context, desired agentv1.DesiredNodeConfig) error {
	a.usageApplyMu.Lock()
	defer a.usageApplyMu.Unlock()
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
	return a.reconciler.Apply(ctx, desired)
}
