// Package healthprobe provides an optional TCP reachability veto for gateway
// candidates. It does not establish protocol health, authorize a candidate, or
// extend an authoritative health lease.
package healthprobe

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"time"
)

var (
	ErrInterval       = errors.New("probe interval must be positive")
	ErrTimeout        = errors.New("probe timeout must be positive and no greater than the interval")
	ErrFailureCount   = errors.New("failure threshold must be between 1 and 1000")
	ErrSuccessCount   = errors.New("success threshold must be between 1 and 1000")
	ErrMaxConcurrency = errors.New("probe concurrency must be between 1 and 4096")
	ErrObserver       = errors.New("probe verdict observer is required")
)

type Target struct {
	ID                   string
	Address              string
	MembershipRevision   uint64
	AuthoritativelyReady bool
	LeaseExpiresAt       time.Time
}

// Verdict is emitted only when the local veto changes. Reachable=false installs
// a veto; Reachable=true removes a prior veto. Neither value changes control
// plane status or lease data.
type Verdict struct {
	ID                 string
	Address            string
	MembershipRevision uint64
	Reachable          bool
	Consecutive        int
}

type DialContext func(context.Context, string, string) (net.Conn, error)
type Observer func(Verdict)
type verdictApplier func(Verdict) error

// ResultObserver runs after the coordinated state transition lock is released.
// It is synchronous, so implementations should remain bounded and non-blocking.
type ResultObserver func(Verdict, error)

type Config struct {
	Interval         time.Duration
	Timeout          time.Duration
	FailureThreshold int
	SuccessThreshold int
	MaxConcurrency   int
	Dial             DialContext
	Observe          Observer
	apply            verdictApplier
	notify           ResultObserver
}

type Monitor struct {
	interval         time.Duration
	timeout          time.Duration
	failureThreshold int
	successThreshold int
	maxConcurrency   int
	dial             DialContext
	observe          Observer
	apply            verdictApplier
	notify           ResultObserver
	transition       sync.Locker

	mu          sync.Mutex
	targets     map[string]targetState
	nextVersion uint64
}

type targetState struct {
	Target
	version              uint64
	consecutiveFailures  int
	consecutiveSuccesses int
	blocked              bool
}

type probeTarget struct {
	Target
	version uint64
}

func New(config Config) (*Monitor, error) {
	if config.Interval <= 0 {
		return nil, ErrInterval
	}
	if config.Timeout <= 0 || config.Timeout > config.Interval {
		return nil, ErrTimeout
	}
	if config.FailureThreshold < 1 || config.FailureThreshold > 1000 {
		return nil, ErrFailureCount
	}
	if config.SuccessThreshold < 1 || config.SuccessThreshold > 1000 {
		return nil, ErrSuccessCount
	}
	if config.MaxConcurrency < 1 || config.MaxConcurrency > 4096 {
		return nil, ErrMaxConcurrency
	}
	if config.Observe == nil && config.apply == nil {
		return nil, ErrObserver
	}
	if config.Dial == nil {
		config.Dial = (&net.Dialer{}).DialContext
	}
	return &Monitor{
		interval: config.Interval, timeout: config.Timeout,
		failureThreshold: config.FailureThreshold, successThreshold: config.SuccessThreshold,
		maxConcurrency: config.MaxConcurrency, dial: config.Dial, observe: config.Observe,
		apply: config.apply, notify: config.notify,
		targets: make(map[string]targetState),
	}, nil
}

// ReplaceTargets installs the latest authoritative view. Probe state survives
// a reload only while endpoint ID and address are unchanged. Readiness and
// lease changes cancel in-flight observations and reset streak counters, but do
// not clear an existing local veto.
func (m *Monitor) ReplaceTargets(targets []Target) {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := make(map[string]targetState, len(targets))
	for _, target := range targets {
		if strings.TrimSpace(target.ID) == "" || strings.TrimSpace(target.Address) == "" || target.MembershipRevision == 0 || target.LeaseExpiresAt.IsZero() {
			continue
		}
		if _, duplicate := next[target.ID]; duplicate {
			continue
		}
		state, exists := m.targets[target.ID]
		if !exists || state.Address != target.Address {
			state = targetState{Target: target, version: m.nextTargetVersionLocked()}
			next[target.ID] = state
			continue
		}
		if state.AuthoritativelyReady != target.AuthoritativelyReady || !state.LeaseExpiresAt.Equal(target.LeaseExpiresAt) {
			state.version = m.nextTargetVersionLocked()
			state.consecutiveFailures = 0
			state.consecutiveSuccesses = 0
		}
		state.Target = target
		next[target.ID] = state
	}
	m.targets = next
}

func (m *Monitor) Run(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.probeCycle(ctx)
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.probeCycle(ctx)
		}
	}
}

func (m *Monitor) probeCycle(ctx context.Context) {
	targets := m.probeTargets(time.Now())
	if len(targets) == 0 || ctx.Err() != nil {
		return
	}
	workers := m.maxConcurrency
	if workers > len(targets) {
		workers = len(targets)
	}
	jobs := make(chan probeTarget)
	var workersDone sync.WaitGroup
	workersDone.Add(workers)
	for range workers {
		go func() {
			defer workersDone.Done()
			for target := range jobs {
				m.probe(ctx, target)
			}
		}()
	}
	for _, target := range targets {
		select {
		case jobs <- target:
		case <-ctx.Done():
			close(jobs)
			workersDone.Wait()
			return
		}
	}
	close(jobs)
	workersDone.Wait()
}

func (m *Monitor) probeTargets(now time.Time) []probeTarget {
	m.mu.Lock()
	defer m.mu.Unlock()
	targets := make([]probeTarget, 0, len(m.targets))
	for _, state := range m.targets {
		if !state.AuthoritativelyReady || !state.LeaseExpiresAt.After(now) {
			continue
		}
		targets = append(targets, probeTarget{Target: state.Target, version: state.version})
	}
	return targets
}

func (m *Monitor) probe(parent context.Context, target probeTarget) {
	probeContext, cancel := context.WithTimeout(parent, m.timeout)
	connection, err := m.dial(probeContext, "tcp", target.Address)
	cancel()
	if connection != nil {
		_ = connection.Close()
	}
	if parent.Err() != nil {
		return
	}
	m.record(target, err == nil)
}

func (m *Monitor) record(target probeTarget, reachable bool) {
	transitionLocked := false
	if m.transition != nil {
		m.transition.Lock()
		transitionLocked = true
	}
	var verdict *Verdict
	m.mu.Lock()
	state, exists := m.targets[target.ID]
	if !exists || state.Address != target.Address || state.MembershipRevision != target.MembershipRevision || state.version != target.version ||
		!state.AuthoritativelyReady || !state.LeaseExpiresAt.After(time.Now()) {
		m.mu.Unlock()
		if transitionLocked {
			m.transition.Unlock()
		}
		return
	}
	if reachable {
		state.consecutiveFailures = 0
		if state.consecutiveSuccesses < m.successThreshold {
			state.consecutiveSuccesses++
		}
		if state.blocked && state.consecutiveSuccesses >= m.successThreshold {
			state.blocked = false
			verdict = &Verdict{
				ID: state.ID, Address: state.Address, MembershipRevision: state.MembershipRevision,
				Reachable: true, Consecutive: state.consecutiveSuccesses,
			}
		}
	} else {
		state.consecutiveSuccesses = 0
		if state.consecutiveFailures < m.failureThreshold {
			state.consecutiveFailures++
		}
		if !state.blocked && state.consecutiveFailures >= m.failureThreshold {
			state.blocked = true
			verdict = &Verdict{
				ID: state.ID, Address: state.Address, MembershipRevision: state.MembershipRevision,
				Reachable: false, Consecutive: state.consecutiveFailures,
			}
		}
	}
	m.targets[target.ID] = state
	m.mu.Unlock()
	var applyErr error
	if verdict != nil {
		if m.apply != nil {
			applyErr = m.apply(*verdict)
		} else {
			m.observe(*verdict)
		}
	}
	if transitionLocked {
		m.transition.Unlock()
	}
	if verdict != nil && m.notify != nil {
		m.notify(*verdict, applyErr)
	}
}

func (m *Monitor) nextTargetVersionLocked() uint64 {
	m.nextVersion++
	return m.nextVersion
}
