package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/config"
	"github.com/hongle/hl-panel/internal/agent/reconciler"
)

func TestHeartbeatCadenceDoesNotAddRequestLatency(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var starts []time.Time
	backoff := reconciler.NewBackoff(config.BackoffConfig{Initial: config.Duration(time.Millisecond), Maximum: config.Duration(time.Second), Multiplier: 2})
	err := runPeriodicCadence(ctx, 120*time.Millisecond, backoff, func(ctx context.Context) error {
		starts = append(starts, time.Now())
		if len(starts) == 4 {
			cancel()
			return ctx.Err()
		}
		return waitContext(ctx, 80*time.Millisecond)
	}, func(err error) { t.Errorf("unexpected error: %v", err) })
	if err != context.Canceled || len(starts) != 4 {
		t.Fatalf("loop result %v, samples %d", err, len(starts))
	}
	// A response-plus-interval loop takes at least 600ms here. Allow scheduler
	// slack while proving request latency is excluded from the healthy cadence.
	if elapsed := starts[3].Sub(starts[0]); elapsed < 350*time.Millisecond || elapsed > 550*time.Millisecond {
		t.Fatalf("three sample intervals took %s", elapsed)
	}
}
