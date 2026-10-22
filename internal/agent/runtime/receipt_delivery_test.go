package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/config"
	"github.com/hongle/hl-panel/internal/agent/reconciler"
)

func receiptTestBackoff(delay time.Duration) *reconciler.Backoff {
	return reconciler.NewBackoff(config.BackoffConfig{
		Initial: config.Duration(delay), Maximum: config.Duration(delay), Multiplier: 2,
	})
}

func waitReceiptCall(t *testing.T, calls <-chan time.Time) time.Time {
	t.Helper()
	select {
	case at := <-calls:
		return at
	case <-time.After(2 * time.Second):
		t.Fatal("receipt worker did not run")
		return time.Time{}
	}
}

func stopReceiptWorker(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("receipt worker cancellation = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("receipt worker did not stop")
	}
}

func TestReceiptDeliveryStartsImmediatelyAndWakesForNewEvidence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{}, 1)
	calls := make(chan time.Time, 4)
	done := make(chan error, 1)
	go func() {
		done <- runReceiptDeliveryLoop(ctx, time.Hour, receiptTestBackoff(time.Second), ready,
			func(context.Context) error { calls <- time.Now(); return nil }, func(error) {})
	}()
	waitReceiptCall(t, calls) // Durable receipts are recovered immediately at startup.
	ready <- struct{}{}
	waitReceiptCall(t, calls) // Does not wait for the one-hour fallback.
	stopReceiptWorker(t, cancel, done)
}

func TestReceiptDeliveryKeepsHintArrivingDuringFlushAndDoesNotOverlap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{}, 1)
	release := make(chan struct{})
	calls := make(chan time.Time, 4)
	done := make(chan error, 1)
	var active, count atomic.Int32
	go func() {
		done <- runReceiptDeliveryLoop(ctx, time.Hour, receiptTestBackoff(time.Second), ready,
			func(ctx context.Context) error {
				if active.Add(1) != 1 {
					t.Error("overlapping receipt delivery")
				}
				defer active.Add(-1)
				calls <- time.Now()
				if count.Add(1) == 1 {
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				return nil
			}, func(error) {})
	}()
	waitReceiptCall(t, calls)
	for i := 0; i < 100; i++ {
		select {
		case ready <- struct{}{}:
		default:
		}
	}
	close(release)
	waitReceiptCall(t, calls)
	stopReceiptWorker(t, cancel, done)
	if got := count.Load(); got != 2 {
		t.Fatalf("coalesced hints triggered %d flushes, want startup plus one", got)
	}
}

func TestReceiptDeliveryHintsCannotBypassFailureBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{}, 1)
	calls := make(chan time.Time, 4)
	reported := make(chan error, 4)
	done := make(chan error, 1)
	var count atomic.Int32
	const delay = 150 * time.Millisecond
	failure := errors.New("control plane unavailable")
	go func() {
		done <- runReceiptDeliveryLoop(ctx, time.Hour, receiptTestBackoff(delay), ready,
			func(context.Context) error {
				calls <- time.Now()
				if count.Add(1) == 1 {
					return failure
				}
				return nil
			}, func(err error) { reported <- err })
	}()
	first := waitReceiptCall(t, calls)
	select {
	case err := <-reported:
		if !errors.Is(err, failure) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("delivery failure not reported")
	}
	for i := 0; i < 100; i++ {
		select {
		case ready <- struct{}{}:
		default:
		}
	}
	second := waitReceiptCall(t, calls)
	if elapsed := second.Sub(first); elapsed < delay {
		t.Fatalf("hint bypassed error backoff: %s", elapsed)
	}
	stopReceiptWorker(t, cancel, done)
	if got := count.Load(); got != 2 || len(reported) != 0 {
		t.Fatalf("stale hints caused extra delivery or reports: calls=%d reports=%d", got, len(reported))
	}
}

func TestReceiptDeliveryPeriodicFallbackWithoutHints(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := make(chan time.Time, 4)
	done := make(chan error, 1)
	go func() {
		done <- runReceiptDeliveryLoop(ctx, 30*time.Millisecond, receiptTestBackoff(time.Second), nil,
			func(context.Context) error { calls <- time.Now(); return nil }, func(error) {})
	}()
	first := waitReceiptCall(t, calls)
	second := waitReceiptCall(t, calls)
	if second.Sub(first) < 30*time.Millisecond {
		t.Fatal("periodic fallback retried too early")
	}
	stopReceiptWorker(t, cancel, done)
}

func TestReceiptDeliveryCancellationStopsBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reported := make(chan time.Time, 1)
	done := make(chan error, 1)
	go func() {
		done <- runReceiptDeliveryLoop(ctx, time.Hour, receiptTestBackoff(time.Hour), nil,
			func(context.Context) error { return errors.New("unavailable") },
			func(error) { reported <- time.Now() })
	}()
	waitReceiptCall(t, reported)
	stopReceiptWorker(t, cancel, done)
}

func TestReceiptDeliveryAlreadyCanceledDoesNotFlush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := runReceiptDeliveryLoop(ctx, time.Hour, receiptTestBackoff(time.Hour), nil,
		func(context.Context) error { t.Fatal("flush after cancellation"); return nil }, func(error) {})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
