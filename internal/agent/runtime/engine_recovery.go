package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hongle/hl-panel/internal/agent/config"
	"github.com/hongle/hl-panel/internal/agent/hostprobe"
	"github.com/hongle/hl-panel/internal/agent/reconciler"
	usageagent "github.com/hongle/hl-panel/internal/agent/usage"
)

var errEngineStopped = errors.New("engine is stopped by administrator")

const (
	// Recovery is a safety path, not a hot poller. A short-lived engine must
	// not cause repeated validation/start attempts that consume CPU or race the
	// listener from the previous process.
	engineRecoveryInterval       = 5 * time.Second
	engineRecoveryMinimumBackoff = 5 * time.Second
	engineRecoveryStableWindow   = 30 * time.Second
)

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
		hostprobe.InvalidateTCP()
		a.logger.Warn("exited engine recovered from last-known-good configuration")
	}
	return err
}

func (a *Agent) recoveryLoop(ctx context.Context) error {
	return runEngineRecoveryLoop(ctx, reconciler.NewBackoff(recoveryBackoffConfig(a.cfg)), a.recoverEngine,
		a.activeEngineMode, func(err error) { a.logger.Warn("local engine recovery failed", "error", err) }, time.Now)
}

func recoveryBackoffConfig(cfg config.Config) config.BackoffConfig {
	settings := cfg.Backoff
	minimum := config.Duration(engineRecoveryMinimumBackoff)
	if settings.Initial.Duration() < engineRecoveryMinimumBackoff {
		settings.Initial = minimum
	}
	if settings.Maximum.Duration() < settings.Initial.Duration() {
		settings.Maximum = settings.Initial
	}
	return settings
}

// runEngineRecoveryLoop keeps a failed or flapping process away from a tight
// restart loop. Backoff is reset only after the process has remained live for
// a full observation window; a single successful Start is not stability.
func runEngineRecoveryLoop(
	ctx context.Context,
	backoff *reconciler.Backoff,
	recover func(context.Context) error,
	active func() string,
	report func(error),
	now func() time.Time,
) error {
	return runEngineRecoveryLoopWithTiming(ctx, backoff, recover, active, report, now,
		engineRecoveryInterval, engineRecoveryStableWindow)
}

func runEngineRecoveryLoopWithTiming(
	ctx context.Context,
	backoff *reconciler.Backoff,
	recover func(context.Context) error,
	active func() string,
	report func(error),
	now func() time.Time,
	interval, stableWindow time.Duration,
) error {
	var healthySince time.Time
	for {
		if !healthySince.IsZero() && active() == "" {
			healthySince = time.Time{}
		}
		if err := recover(ctx); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				if ctx.Err() != nil {
					return ctx.Err()
				}
			}
			report(err)
			healthySince = time.Time{}
			if err := waitContext(ctx, backoff.Next()); err != nil {
				return err
			}
			continue
		}

		if active() != "" {
			if healthySince.IsZero() {
				healthySince = now()
			}
			if now().Sub(healthySince) >= stableWindow {
				backoff.Reset()
			}
		} else {
			healthySince = time.Time{}
		}
		if err := waitContext(ctx, interval); err != nil {
			return err
		}
	}
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
	err := a.closeEngines(ctx)
	hostprobe.InvalidateTCP()
	return err
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
		err = a.reconciler.RecoverPending(ctx)
	} else {
		err = a.reconciler.RestoreApplied(ctx)
	}
	hostprobe.InvalidateTCP()
	return err
}
