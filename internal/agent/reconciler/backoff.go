package reconciler

import (
	"math"
	"math/rand/v2"
	"time"

	"github.com/hongle/hl-panel/internal/agent/config"
)

type Backoff struct {
	initial    time.Duration
	maximum    time.Duration
	multiplier float64
	jitter     float64
	current    time.Duration
	randFloat  func() float64
}

func NewBackoff(settings config.BackoffConfig) *Backoff {
	return &Backoff{
		initial:    settings.Initial.Duration(),
		maximum:    settings.Maximum.Duration(),
		multiplier: settings.Multiplier,
		jitter:     settings.JitterFraction,
		randFloat:  rand.Float64,
	}
}

func (b *Backoff) Reset() {
	b.current = 0
}

func (b *Backoff) Next() time.Duration {
	if b.current == 0 {
		b.current = b.initial
	} else {
		next := float64(b.current) * b.multiplier
		if next > float64(b.maximum) || next > float64(math.MaxInt64) {
			b.current = b.maximum
		} else {
			b.current = time.Duration(next)
		}
	}
	if b.jitter == 0 || b.randFloat == nil {
		return b.current
	}
	factor := 1 - b.jitter + 2*b.jitter*b.randFloat()
	value := time.Duration(float64(b.current) * factor)
	if value < 0 {
		return 0
	}
	return value
}
