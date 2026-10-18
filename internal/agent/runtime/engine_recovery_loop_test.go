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

func TestRecoveryBackoffConfigHasMinimumDelay(t *testing.T) {
	settings := recoveryBackoffConfig(config.Config{Backoff: config.BackoffConfig{
		Initial: config.Duration(time.Second), Maximum: config.Duration(2 * time.Second), Multiplier: 2,
	}})
	if settings.Initial.Duration() != engineRecoveryMinimumBackoff {
		t.Fatalf("recovery initial = %s, want %s", settings.Initial.Duration(), engineRecoveryMinimumBackoff)
	}
	if settings.Maximum.Duration() != engineRecoveryMinimumBackoff {
		t.Fatalf("recovery maximum = %s, want %s", settings.Maximum.Duration(), engineRecoveryMinimumBackoff)
	}
}

func TestEngineRecoveryLoopKeepsBackoffAfterShortLivedRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	callTimes := make([]time.Time, 0, 4)
	recover := func(context.Context) error {
		n := calls.Add(1)
		callTimes = append(callTimes, time.Now())
		switch n {
		case 1:
			return errors.New("initial recovery failure")
		case 2:
			return nil
		case 3:
			return errors.New("process exited during recovery window")
		case 4:
			cancel()
			return nil
		default:
			return nil
		}
	}
	active := func() string {
		if n := calls.Load(); n >= 2 && n <= 4 {
			return "xray"
		}
		return ""
	}
	backoff := reconciler.NewBackoff(config.BackoffConfig{
		Initial: config.Duration(40 * time.Millisecond), Maximum: config.Duration(160 * time.Millisecond), Multiplier: 2,
	})
	err := runEngineRecoveryLoopWithTiming(ctx, backoff, recover, active, func(error) {}, time.Now,
		5*time.Millisecond, 100*time.Millisecond)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("recovery loop error = %v, want context cancellation", err)
	}
	if got := calls.Load(); got != 4 {
		t.Fatalf("recovery calls = %d, want 4", got)
	}
	if delay := callTimes[3].Sub(callTimes[2]); delay < 70*time.Millisecond {
		t.Fatalf("retry after short-lived recovery waited %s, want backoff instead of tight retry", delay)
	}
}
