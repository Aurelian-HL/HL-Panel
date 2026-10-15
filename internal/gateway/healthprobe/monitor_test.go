package healthprobe

import (
	"context"
	"errors"
	"net"
	"sync"
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

func TestMonitorTreatsNilConnectionWithoutErrorAsUnreachable(t *testing.T) {
	verdicts := make(chan Verdict, 1)
	monitor, err := New(Config{
		Interval: time.Second, Timeout: time.Second,
		FailureThreshold: 1, SuccessThreshold: 1, MaxConcurrency: 1,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, nil
		},
		Observe: func(verdict Verdict) { verdicts <- verdict },
	})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ID: "node-a", Address: "127.0.0.1:10001", MembershipRevision: 1,
		AuthoritativelyReady: true, LeaseExpiresAt: time.Now().Add(time.Minute)}
	monitor.ReplaceTargets([]Target{target})
	monitor.probeCycle(context.Background())
	select {
	case verdict := <-verdicts:
		if verdict.Reachable || verdict.ID != target.ID {
			t.Fatalf("nil connection verdict = %#v, want unreachable", verdict)
		}
	case <-time.After(time.Second):
		t.Fatal("nil connection was accepted as reachable")
	}
}

func TestMonitorPreservesFailureStreakAcrossLeaseRenewal(t *testing.T) {
	verdicts := make(chan Verdict, 1)
	monitor, err := New(Config{
		Interval: time.Second, Timeout: time.Second,
		FailureThreshold: 3, SuccessThreshold: 1, MaxConcurrency: 1,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("connection refused")
		},
		Observe: func(verdict Verdict) { verdicts <- verdict },
	})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{
		ID: "node-a", Address: "127.0.0.1:10001", MembershipRevision: 1,
		AuthoritativelyReady: true, LeaseExpiresAt: time.Now().Add(time.Minute),
	}
	monitor.ReplaceTargets([]Target{target})
	probe := monitor.probeTargets(time.Now())[0]
	monitor.record(probe, false)

	// A control-plane write advances the snapshot revision while renewing the
	// lease. This must not erase evidence already accumulated for the endpoint.
	target.MembershipRevision = 2
	target.LeaseExpiresAt = time.Now().Add(2 * time.Minute)
	monitor.ReplaceTargets([]Target{target})
	monitor.record(monitor.probeTargets(time.Now())[0], false)
	select {
	case verdict := <-verdicts:
		t.Fatalf("failure threshold reached too early: %#v", verdict)
	default:
	}

	monitor.record(monitor.probeTargets(time.Now())[0], false)
	verdict := waitVerdict(t, verdicts)
	if verdict.Reachable || verdict.Consecutive != 3 {
		t.Fatalf("renewed lease did not preserve failure streak: %#v", verdict)
	}
}

func TestMonitorKeepsStreakAndInFlightResultAcrossUnrelatedRevision(t *testing.T) {
	verdicts := make(chan Verdict, 2)
	monitor, err := New(Config{
		Interval: time.Second, Timeout: time.Second,
		FailureThreshold: 2, SuccessThreshold: 2, MaxConcurrency: 1,
		Observe: func(verdict Verdict) { verdicts <- verdict },
	})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{
		ID: "node-a", Address: "127.0.0.1:10001", MembershipRevision: 1,
		AuthoritativelyReady: true, LeaseExpiresAt: time.Now().Add(time.Minute),
	}
	monitor.ReplaceTargets([]Target{target})
	old := monitor.probeTargets(time.Now())[0]
	monitor.record(old, false)

	// The snapshot revision also advances for unrelated database writes. The
	// endpoint address and continuous lease are unchanged, so the old TCP result
	// remains valid and must be applied using the current revision.
	target.MembershipRevision = 2
	monitor.ReplaceTargets([]Target{target})
	monitor.record(old, false)
	verdict := waitVerdict(t, verdicts)
	if verdict.Reachable || verdict.Consecutive != 2 || verdict.MembershipRevision != 2 {
		t.Fatalf("continuous target revision failure verdict = %#v", verdict)
	}
}

func TestMonitorContinuousRenewalKeepsRecoveryStreakAndAcceptsSameTargetResult(t *testing.T) {
	verdicts := make(chan Verdict, 2)
	monitor, err := New(Config{
		Interval: time.Second, Timeout: time.Second,
		FailureThreshold: 1, SuccessThreshold: 2, MaxConcurrency: 1,
		Observe: func(verdict Verdict) { verdicts <- verdict },
	})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ID: "node-a", Address: "127.0.0.1:10001", MembershipRevision: 1,
		AuthoritativelyReady: true, LeaseExpiresAt: time.Now().Add(time.Minute)}
	monitor.ReplaceTargets([]Target{target})
	old := monitor.probeTargets(time.Now())[0]
	monitor.record(old, false)
	_ = waitVerdict(t, verdicts)
	monitor.record(old, true)
	target.LeaseExpiresAt = time.Now().Add(2 * time.Minute)
	target.MembershipRevision = 2
	monitor.ReplaceTargets([]Target{target})
	monitor.record(old, true)
	if verdict := waitVerdict(t, verdicts); !verdict.Reachable || verdict.Consecutive != 2 || verdict.MembershipRevision != 2 {
		t.Fatalf("renewed lease lost completed recovery: %#v", verdict)
	}
}

func TestMonitorAuthorityBreakDiscardsCompletedSuccesses(t *testing.T) {
	for _, change := range []string{"unready", "shortened", "expired"} {
		t.Run(change, func(t *testing.T) {
			verdicts := make(chan Verdict, 2)
			monitor, err := New(Config{Interval: time.Second, Timeout: time.Second,
				FailureThreshold: 1, SuccessThreshold: 2, MaxConcurrency: 1,
				Observe: func(v Verdict) { verdicts <- v }})
			if err != nil {
				t.Fatal(err)
			}
			target := Target{ID: "node-a", Address: "127.0.0.1:10001", MembershipRevision: 1,
				AuthoritativelyReady: true, LeaseExpiresAt: time.Now().Add(time.Minute)}
			monitor.ReplaceTargets([]Target{target})
			old := monitor.probeTargets(time.Now())[0]
			monitor.record(old, false)
			_ = waitVerdict(t, verdicts)
			monitor.record(old, true)
			switch change {
			case "unready":
				target.AuthoritativelyReady = false
			case "shortened":
				target.LeaseExpiresAt = time.Now().Add(30 * time.Second)
			case "expired":
				target.LeaseExpiresAt = time.Now().Add(-time.Second)
			}
			monitor.ReplaceTargets([]Target{target})
			target.AuthoritativelyReady = true
			target.LeaseExpiresAt = time.Now().Add(2 * time.Minute)
			monitor.ReplaceTargets([]Target{target})
			current := monitor.probeTargets(time.Now())[0]
			monitor.record(current, true)
			select {
			case verdict := <-verdicts:
				t.Fatalf("authority break reused old success: %#v", verdict)
			default:
			}
			monitor.record(current, true)
			if verdict := waitVerdict(t, verdicts); !verdict.Reachable {
				t.Fatalf("fresh successes did not recover: %#v", verdict)
			}
		})
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

func TestMonitorBoundsCycleAndRotatesLargeMembership(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	monitor, err := New(Config{
		Interval: 30 * time.Millisecond, Timeout: 20 * time.Millisecond,
		FailureThreshold: 1, SuccessThreshold: 1, MaxConcurrency: 1,
		Dial: func(ctx context.Context, _, address string) (net.Conn, error) {
			mu.Lock()
			calls = append(calls, address)
			mu.Unlock()
			<-ctx.Done()
			return nil, ctx.Err()
		},
		Observe: func(Verdict) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	monitor.ReplaceTargets([]Target{
		{ID: "node-a", Address: "a:1", MembershipRevision: 1, AuthoritativelyReady: true, LeaseExpiresAt: time.Now().Add(time.Minute)},
		{ID: "node-b", Address: "b:1", MembershipRevision: 1, AuthoritativelyReady: true, LeaseExpiresAt: time.Now().Add(time.Minute)},
		{ID: "node-c", Address: "c:1", MembershipRevision: 1, AuthoritativelyReady: true, LeaseExpiresAt: time.Now().Add(time.Minute)},
	})

	started := time.Now()
	monitor.probeCycle(context.Background())
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("probe cycle took %s, want it bounded near interval", elapsed)
	}
	monitor.probeCycle(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if len(calls) < 3 {
		t.Fatalf("probe calls = %#v, want the next cycle to reach the previously unscheduled target", calls)
	}
	if calls[0] != "a:1" || calls[1] != "b:1" || calls[2] != "c:1" {
		t.Fatalf("probe order = %#v, want a fair a, b, c rotation", calls)
	}
}

func TestMonitorRetriesTransitionAfterApplyFailure(t *testing.T) {
	var applyCalls atomic.Int32
	var applyErr atomic.Bool
	monitor, err := New(Config{
		Interval: time.Second, Timeout: time.Second,
		FailureThreshold: 1, SuccessThreshold: 1, MaxConcurrency: 1,
		apply: func(Verdict) error {
			applyCalls.Add(1)
			if applyErr.Load() {
				return errors.New("router temporarily unavailable")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ID: "node-a", Address: "127.0.0.1:10001", MembershipRevision: 1,
		AuthoritativelyReady: true, LeaseExpiresAt: time.Now().Add(time.Minute)}
	monitor.ReplaceTargets([]Target{target})
	probe := monitor.probeTargets(time.Now())[0]
	applyErr.Store(true)
	monitor.record(probe, false)
	if blocked, ok := monitor.isBlocked(target.ID); !ok || blocked {
		t.Fatalf("state after failed veto apply = blocked=%v ok=%v, want unblocked", blocked, ok)
	}
	applyErr.Store(false)
	monitor.record(probe, false)
	if blocked, ok := monitor.isBlocked(target.ID); !ok || !blocked {
		t.Fatalf("state after successful retry = blocked=%v ok=%v, want blocked", blocked, ok)
	}
	if applyCalls.Load() != 2 {
		t.Fatalf("apply calls = %d, want two attempts", applyCalls.Load())
	}
}

func (m *Monitor) isBlocked(id string) (bool, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.targets[id]
	return state.blocked, ok
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
