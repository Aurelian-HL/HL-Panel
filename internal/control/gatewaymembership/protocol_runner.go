package gatewaymembership

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/protocolprobe"
	"github.com/hongle/hl-panel/internal/serviceaddress"
)

// ProbeState is confidential scheduling input, never a public API response.
type ProbeState struct {
	State  State
	Config ProtocolProbeConfig
}

type protocolRunnerRepository interface {
	Repository
	ProtocolProbeConfigurator
	ProtocolProbeStates(context.Context) ([]ProbeState, error)
	PublishProtocolHealthBatch(context.Context, []ProtocolObservation) ([]error, error)
}

const protocolProbeWorkers = 8

type ProtocolRunner struct {
	repository protocolRunnerRepository
	observer   *ProtocolObserver
	xrayBinary string
	echoPort   int
	interval   time.Duration
	logger     *slog.Logger
	wake       chan struct{}
}

type probeJob struct {
	key     string
	input   ProbeState
	member  endpoints.EndpointPoolMember
	attempt uint64
}
type scheduledProbe struct {
	job      probeJob
	due      time.Time
	inFlight uint64
	priority int
}
type probeResult struct {
	job         probeJob
	observation ProtocolObservation
	err         error
}

func NewProtocolRunner(repository protocolRunnerRepository, xrayBinary string, echoPort int, interval time.Duration, logger *slog.Logger) *ProtocolRunner {
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 || interval > endpoints.DefaultHealthTTL/3 {
		interval = 15 * time.Second
	}
	return &ProtocolRunner{repository: repository, observer: NewProtocolObserver(repository, time.Now), xrayBinary: xrayBinary, echoPort: echoPort, interval: interval, logger: logger, wake: make(chan struct{}, 1)}
}

func (r *ProtocolRunner) NotifyDeployment(_ string) {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Run schedules each rule/node independently. A slow or failing pool cannot
// block all other pools for an entire scan or delay lease renewal by minutes.
// Network concurrency and database publication are bounded separately.
func (r *ProtocolRunner) Run(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	jobs := make(chan probeJob)
	results := make(chan probeResult, protocolProbeWorkers)
	var workers sync.WaitGroup
	for i := 0; i < protocolProbeWorkers; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case job := <-jobs:
					state := job.input.State
					spec := protocolprobe.Spec{XrayBinary: r.xrayBinary, DialHost: job.member.DialHost, DialPort: state.Rule.ListenPort, UUID: job.input.Config.UUID, ServerName: state.Rule.RealityServerName, PublicKey: state.Rule.RealityPublicKey, ShortID: state.Rule.RealityShortID, EchoHost: "127.0.0.1", EchoPort: job.input.Config.EchoPort}
					observation, err := r.observer.observeState(ctx, state, job.input.Config, job.member.NodeID, spec)
					select {
					case results <- probeResult{job, observation, err}:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}
	defer func() { cancel(); workers.Wait() }()
	targets := make(map[string]*scheduledProbe)
	refresh := func() {
		if err := r.refresh(ctx, targets); err != nil && !errors.Is(err, context.Canceled) {
			r.logger.Warn("VLESS protocol scheduling refresh failed", "error", err)
		}
	}
	refresh()
	refreshTicker := time.NewTicker(r.interval)
	defer refreshTicker.Stop()
	flushTicker := time.NewTicker(time.Second)
	defer flushTicker.Stop()
	pending := make([]probeResult, 0, 32)
	var dispatchSequence uint64
	flush := func() {
		if len(pending) == 0 {
			return
		}
		observations := make([]ProtocolObservation, len(pending))
		for i, result := range pending {
			observations[i] = result.observation
		}
		rejected, err := r.repository.PublishProtocolHealthBatch(ctx, observations)
		if err != nil {
			r.logger.Warn("VLESS protocol health batch failed", "count", len(observations), "error", err)
		}
		for i, result := range pending {
			failure := err
			if failure == nil && i < len(rejected) {
				failure = rejected[i]
			}
			if failure != nil {
				if target := targets[result.job.key]; target != nil && sameProbe(target.job, result.job) {
					target.due = time.Now().Add(r.interval)
				}
			}
		}
		if err == nil {
			r.logger.Debug("VLESS protocol health batch published", "count", len(observations))
		}
		pending = pending[:0]
	}
	for {
		// Select the earliest due probe; prioritize one candidate from each pool
		// before probing its remaining members, including newly deployed rules.
		next := nextProbe(targets, time.Now())
		var send chan probeJob
		var job probeJob
		if next != nil {
			send = jobs
			job = next.job
			job.attempt = dispatchSequence + 1
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-r.wake:
			refresh()
		case <-refreshTicker.C:
			refresh()
		case <-flushTicker.C:
			flush()
		case send <- job:
			dispatchSequence = job.attempt
			next.inFlight = job.attempt
		case result := <-results:
			target := targets[result.job.key]
			// A retired target may have been re-added while its old worker ran.
			// Only the matching dispatch owns this target's in-flight slot.
			if target == nil || target.inFlight != result.job.attempt {
				continue
			}
			target.inFlight = 0
			// A refresh may replace this job while its challenge is in flight.
			// Never let an old completion delay probing the new configuration.
			if target != nil && !sameProbe(target.job, result.job) {
				continue
			}
			if target != nil {
				target.due = time.Now().Add(endpoints.DefaultHealthTTL / 3)
			}
			if result.err != nil {
				if target != nil {
					target.due = time.Now().Add(r.interval)
				}
				r.logger.Debug("VLESS protocol observation failed", "pool_id", result.job.input.State.Pool.ID, "node_id", result.job.member.NodeID, "error", result.err)
			} else {
				pending = append(pending, result)
				if len(pending) >= 32 {
					flush()
				}
			}
		}
	}
}

func nextProbe(targets map[string]*scheduledProbe, now time.Time) *scheduledProbe {
	var next *scheduledProbe
	for _, target := range targets {
		if target.inFlight != 0 || target.due.After(now) {
			continue
		}
		if next == nil || target.priority < next.priority || (target.priority == next.priority &&
			(target.due.Before(next.due) || (target.due.Equal(next.due) && target.job.key < next.job.key))) {
			next = target
		}
	}
	return next
}

func sameProbe(a, b probeJob) bool {
	x, y := a.input.State, b.input.State
	ex, ey := x.Deployments[a.member.NodeID].Status, y.Deployments[b.member.NodeID].Status
	return x.Rule.Revision == y.Rule.Revision && ex.NodeConfigGeneration == ey.NodeConfigGeneration && ex.ConfigSHA256 == ey.ConfigSHA256 && a.member.DialHost == b.member.DialHost && a.input.Config == b.input.Config
}

// Prefer a recently successful address when a bundle change makes the pool
// unready. This is a scheduling hint only: old proof cannot authorize the new
// bundle, and the selected address must pass a fresh protocol challenge.
func recoveryProbeCandidate(state State, now time.Time) string {
	var fallback, preferred string
	var verifiedAt time.Time
	for _, member := range state.Members {
		if member.State != endpoints.CandidateEligible || member.Weight < 1 ||
			!deploymentReady(state.Deployments[member.NodeID], state.Rule, member.NodeID) {
			continue
		}
		if fallback == "" {
			fallback = member.NodeID
		}
		proof := state.ProtocolHealth[member.NodeID]
		host, err := serviceaddress.NormalizeHost(member.DialHost)
		observedHost, proofErr := serviceaddress.NormalizeHost(proof.DialHost)
		if err != nil || proofErr != nil || host == "" || host != observedHost ||
			proof.RuleID != state.Rule.ID || proof.NodeID != member.NodeID || proof.RuleRevision != state.Rule.Revision ||
			proof.Protocol != forwarding.IngressVLESSReality || proof.VerifiedAt.IsZero() || proof.VerifiedAt.After(now) ||
			!proof.LeaseExpiresAt.After(proof.VerifiedAt) || proof.LeaseExpiresAt.After(proof.VerifiedAt.Add(endpoints.DefaultHealthTTL)) {
			continue
		}
		if !proof.LeaseExpiresAt.After(now) && !protocolEvidenceMatches(proof, state.Deployments[member.NodeID].Status, state.Rule, member, host, now) {
			continue
		}
		if preferred == "" || proof.VerifiedAt.After(verifiedAt) {
			preferred, verifiedAt = member.NodeID, proof.VerifiedAt
		}
	}
	if preferred != "" {
		return preferred
	}
	return fallback
}

func (r *ProtocolRunner) refresh(ctx context.Context, targets map[string]*scheduledProbe) error {
	inputs, err := r.repository.ProtocolProbeStates(ctx)
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	now := time.Now()
	for _, input := range inputs {
		state := input.State
		if state.Rule.Paused || state.Rule.IngressReadiness() != forwarding.IngressReady || (state.Rule.Status != forwarding.StatusPendingActivation && state.Rule.Status != forwarding.StatusActive) {
			continue
		}
		if input.Config.UUID == "" {
			config, err := NewProtocolProbeConfig(r.echoPort)
			if err != nil {
				return err
			}
			if err := r.repository.ConfigureProtocolProbe(ctx, state.Rule.ID, config); err != nil && !errors.Is(err, faults.ErrConflict) {
				return err
			}
			continue // New bundle must be applied before it can authorize a challenge.
		}
		recoveryCandidate := recoveryProbeCandidate(state, now.UTC())
		poolReady := ReadyCandidateCount(state, now.UTC()) > 0
		for _, member := range state.Members {
			if member.State != endpoints.CandidateEligible || member.Weight < 1 || !deploymentReady(state.Deployments[member.NodeID], state.Rule, member.NodeID) {
				continue
			}
			key := state.Pool.ID + ":" + member.NodeID
			job := probeJob{key: key, input: input, member: member}
			priority := 1
			// Restore one candidate per unready pool before filling the remaining
			// nodes. Once ready, prioritize renewal of current proven candidates.
			if member.NodeID == recoveryCandidate || (poolReady && protocolEvidenceMatches(state.ProtocolHealth[member.NodeID], state.Deployments[member.NodeID].Status, state.Rule, member, member.DialHost, now.UTC())) {
				priority = 0
			}
			seen[key] = true
			if previous := targets[key]; previous != nil {
				if !sameProbe(previous.job, job) {
					previous.due = now
				}
				previous.job = job
				previous.priority = priority
				continue
			}
			// Pool priorities provide fairness without delaying idle workers.
			due := now
			if health := state.ProtocolHealth[member.NodeID]; protocolHealthy(health, state.Deployments[member.NodeID].Status, state.Rule, member, member.DialHost, now.UTC()) {
				renew := health.VerifiedAt.Add(endpoints.DefaultHealthTTL / 3)
				if renew.After(due) {
					due = renew
				}
			}
			targets[key] = &scheduledProbe{job: job, due: due, priority: priority}
		}
	}
	for key := range targets {
		if !seen[key] {
			delete(targets, key)
		}
	}
	r.logger.Debug("VLESS protocol scheduling refreshed", "pools", len(inputs), "targets", len(targets))
	return nil
}

func NewProtocolProbeConfig(echoPort int) (ProtocolProbeConfig, error) {
	if echoPort < 1 || echoPort > 65535 {
		return ProtocolProbeConfig{}, fmt.Errorf("probe echo port is invalid")
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return ProtocolProbeConfig{}, fmt.Errorf("generate protocol probe identity: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(raw[:])
	return ProtocolProbeConfig{UUID: fmt.Sprintf("%s-%s-%s-%s-%s", encoded[0:8], encoded[8:12], encoded[12:16], encoded[16:20], encoded[20:32]), EchoPort: echoPort}, nil
}
