package runtime

import (
	"context"
	"time"

	"github.com/hongle/hl-panel/internal/agent/config"
	"github.com/hongle/hl-panel/internal/agent/reconciler"
)

func (a *Agent) receiptLoop(ctx context.Context) error {
	interval := a.cfg.DesiredPollInterval.Duration()
	if interval <= 0 || interval > 5*time.Second {
		interval = 5 * time.Second
	}
	settings := a.cfg.Backoff
	if settings.Maximum.Duration() <= 0 || settings.Maximum.Duration() > heartbeatRetryMaximum {
		settings.Maximum = config.Duration(heartbeatRetryMaximum)
	}
	if settings.Initial.Duration() <= 0 || settings.Initial.Duration() > settings.Maximum.Duration() {
		settings.Initial = settings.Maximum
	}
	return runReceiptDeliveryLoop(ctx, interval, reconciler.NewBackoff(settings), a.reconciler.ReceiptReady(),
		a.reconciler.FlushReceipts, func(err error) { a.logger.Warn("apply receipt delivery failed", "error", err) })
}

// One worker handles both durable startup recovery and coalesced local hints.
// Hints shorten the idle wait, but never bypass error backoff. No additional
// goroutine or network request is created for each incoming desired generation.
func runReceiptDeliveryLoop(ctx context.Context, interval time.Duration, backoff *reconciler.Backoff,
	ready <-chan struct{}, flush func(context.Context) error, report func(error)) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Any hint already queued is covered by the upcoming durable queue read.
		select {
		case <-ready:
		default:
		}
		if err := flush(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			report(err)
			if err := waitContext(ctx, backoff.Next()); err != nil {
				return err
			}
			continue
		}
		backoff.Reset()
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-ready:
		case <-timer.C:
		}
		timer.Stop()
	}
}
