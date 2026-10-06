package healthprobe

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestMonitorAppliesFailureAndRecoveryThresholdsAcrossReload(t *testing.T) {
	var reachable atomic.Bool
	verdicts := make(chan Verdict, 4)
	monitor, err := New(Config{
		Interval: 10 * time.Millisecond, Timeout: 5 * time.Millisecond,
		FailureThreshold: 2, SuccessThreshold: 2, MaxConcurrency: 2,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			if !reachable.Load() {
				return nil, errors.New("connection refused")
			}
			left, right := net.Pipe()
			_ = right.Close()
			return left, nil
		},
		Observe: func(verdict Verdict) { verdicts <- verdict },
	})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ID: "node-a", Address: "127.0.0.1:10001", MembershipRevision: 1, AuthoritativelyReady: true, LeaseExpiresAt: time.Now().Add(time.Minute)}
	monitor.ReplaceTargets([]Target{target})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		monitor.Run(ctx)
		close(done)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("monitor did not stop")
		}
	}()

	unreachable := waitVerdict(t, verdicts)
	if unreachable.ID != target.ID || unreachable.Reachable || unreachable.Consecutive != 2 {
		t.Fatalf("unreachable verdict = %#v", unreachable)
	}
	// A same-address authoritative reload renews the lease but must not erase
	// the local veto. Recovery still requires the configured success threshold.
	target.MembershipRevision = 2
	target.LeaseExpiresAt = time.Now().Add(2 * time.Minute)
	monitor.ReplaceTargets([]Target{target})
	reachable.Store(true)
	recovered := waitVerdict(t, verdicts)
	if recovered.ID != target.ID || !recovered.Reachable || recovered.Consecutive != 2 {
		t.Fatalf("recovery verdict = %#v", recovered)
	}
}

func TestMonitorIgnoresInFlightResultAfterLeaseExpires(t *testing.T) {
	verdicts := make(chan Verdict, 2)
	monitor, err := New(Config{
		Interval: time.Second, Timeout: time.Second,
		FailureThreshold: 1, SuccessThreshold: 1, MaxConcurrency: 1,
		Observe: func(verdict Verdict) { verdicts <- verdict },
	})
	if err != nil {
		t.Fatal(err)
	}
	leaseExpiresAt := time.Now().Add(100 * time.Millisecond)
	monitor.ReplaceTargets([]Target{{
		ID: "node-a", Address: "127.0.0.1:10001", MembershipRevision: 1,
		AuthoritativelyReady: true, LeaseExpiresAt: leaseExpiresAt,
	}})
	probeTargets := monitor.probeTargets(time.Now())
	if len(probeTargets) != 1 {
		t.Fatalf("probe target count = %d, want one", len(probeTargets))
	}
	monitor.record(probeTargets[0], false)
	if verdict := waitVerdict(t, verdicts); verdict.Reachable {
		t.Fatalf("initial verdict = %#v, want local veto", verdict)
	}
	if delay := time.Until(leaseExpiresAt); delay > 0 {
		time.Sleep(delay + 10*time.Millisecond)
	}
	monitor.record(probeTargets[0], true)
	select {
	case verdict := <-verdicts:
		t.Fatalf("expired in-flight result changed veto: %#v", verdict)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestMonitorDoesNotProbeUnreadyOrExpiredTargets(t *testing.T) {
	var dials atomic.Int64
	monitor, err := New(Config{
		Interval: 5 * time.Millisecond, Timeout: 5 * time.Millisecond,
		FailureThreshold: 1, SuccessThreshold: 1, MaxConcurrency: 1,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			dials.Add(1)
			return nil, errors.New("must not dial")
		},
		Observe: func(Verdict) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	monitor.ReplaceTargets([]Target{
		{ID: "offline", Address: "127.0.0.1:10001", MembershipRevision: 1, LeaseExpiresAt: time.Now().Add(time.Minute)},
		{ID: "expired", Address: "127.0.0.1:10002", MembershipRevision: 1, AuthoritativelyReady: true, LeaseExpiresAt: time.Now().Add(-time.Second)},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	monitor.Run(ctx)
	if dials.Load() != 0 {
		t.Fatalf("dial count = %d, want zero", dials.Load())
	}
}

func TestMonitorCancelsInFlightDialOnShutdown(t *testing.T) {
	started := make(chan struct{})
	monitor, err := New(Config{
		Interval: time.Second, Timeout: time.Second,
		FailureThreshold: 1, SuccessThreshold: 1, MaxConcurrency: 1,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		},
		Observe: func(Verdict) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	monitor.ReplaceTargets([]Target{{
		ID: "node-a", Address: "127.0.0.1:10001", MembershipRevision: 1,
		AuthoritativelyReady: true, LeaseExpiresAt: time.Now().Add(time.Minute),
	}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		monitor.Run(ctx)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("probe dial did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("monitor did not cancel in-flight dial")
	}
}

func TestNewValidatesProbeConfiguration(t *testing.T) {
	valid := Config{
		Interval: time.Second, Timeout: time.Second,
		FailureThreshold: 1, SuccessThreshold: 1, MaxConcurrency: 1,
		Observe: func(Verdict) {},
	}
	mutations := []func(*Config){
		func(config *Config) { config.Interval = 0 },
		func(config *Config) { config.Timeout = 0 },
		func(config *Config) { config.Timeout = 2 * time.Second },
		func(config *Config) { config.FailureThreshold = 0 },
		func(config *Config) { config.SuccessThreshold = 0 },
		func(config *Config) { config.MaxConcurrency = 0 },
		func(config *Config) { config.Observe = nil },
	}
	for _, mutate := range mutations {
		config := valid
		mutate(&config)
		if _, err := New(config); err == nil {
			t.Fatal("New() accepted invalid probe configuration")
		}
	}
	if _, err := New(valid); err != nil {
		t.Fatal(err)
	}
}

func waitVerdict(t *testing.T, verdicts <-chan Verdict) Verdict {
	t.Helper()
	select {
	case verdict := <-verdicts:
		return verdict
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for probe verdict")
		return Verdict{}
	}
}
