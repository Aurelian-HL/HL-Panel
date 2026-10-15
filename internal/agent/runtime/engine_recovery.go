package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hongle/hl-panel/internal/agent/reconciler"
	usageagent "github.com/hongle/hl-panel/internal/agent/usage"
)

var errEngineStopped = errors.New("engine is stopped by administrator")

const engineRecoveryInterval = time.Second

// Recovery does not depend on a desired-generation response or on report
// delivery. It shares the lifecycle lock with apply and administrator commands.
func (a *Agent) recoverEngine(ctx context.Context) error {
	a.usageApplyMu.Lock()
	defer a.usageApplyMu.Unlock()
	stopped, err := a.state.EngineStopped()
	if err != nil || stopped {
		return err
	}
	restored, err := a.reconciler.EnsureApplied(ctx)
	if restored && err == nil {
		a.logger.Warn("exited engine recovered from last-known-good configuration")
	}
	return err
}

func (a *Agent) recoveryLoop(ctx context.Context) error {
	return runPeriodic(ctx, engineRecoveryInterval, reconciler.NewBackoff(a.cfg.Backoff), a.recoverEngine,
		func(err error) { a.logger.Warn("local engine recovery failed", "error", err) })
}

func (a *Agent) stopEngine(ctx context.Context) error {
	a.usageApplyMu.Lock()
	defer a.usageApplyMu.Unlock()
	// Persist before stopping, so neither a concurrent apply nor a later agent
	// restart can undo an explicit administrator action.
	if err := a.state.SetEngineStopped(true); err != nil {
		return err
	}
	if a.usageReporter != nil {
		if err := a.usageReporter.CollectOnce(ctx); err != nil {
			a.logger.Warn("final usage collection before engine stop failed", "error", err)
		}
	}
	return a.closeEngines(ctx)
}

func (a *Agent) restartEngine(ctx context.Context) error {
	a.usageApplyMu.Lock()
	defer a.usageApplyMu.Unlock()
	if a.usageReporter != nil {
		if err := a.usageReporter.CollectOnce(ctx); err != nil {
			if !errors.Is(err, usageagent.ErrCounterReadFailed) {
				return fmt.Errorf("%w: %w", errUsageBeforeApply, err)
			}
			a.logger.Warn("final usage collection before engine restart failed", "error", err)
		}
	}
	if err := a.state.SetEngineStopped(false); err != nil {
		return err
	}
	_, pending, err := a.state.PendingDesired()
	if err != nil {
		return err
	}
	if pending {
		return a.reconciler.RecoverPending(ctx)
	}
	return a.reconciler.RestoreApplied(ctx)
}
