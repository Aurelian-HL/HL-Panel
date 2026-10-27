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

	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
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

const protocolProbeWorkers = protocolprobe.NetworkChecks

// Fairness is independent of concurrency: raising throughput must not reduce
// the share of probes reserved for other members of an already recovered pool.
const protocolPriorityBurst = 8

// Protocol observations are published outside the scheduler loop. A blocked
// database write must not stop probes, lease renewal, or failure detection.
const (
	protocolPublishQueueSize = 1
	protocolPendingLimit     = 256
	// A repository call must not occupy the publisher or refresh worker until
	// the 90-second protocol lease has expired. PostgreSQL currently applies a
	// similar transaction timeout, but keeping the bound here protects the
	// scheduler when another repository implementation or a saturated pool
	// fails to enforce its own context promptly.
	protocolRepositoryTimeout = 8 * time.Second
	// A failed publication must be retried frequently enough to renew a
	// 90-second health lease. A long exponential backoff can otherwise place
	// the next attempt after every observation in the batch has expired.
	protocolRetryDelayCap = endpoints.DefaultHealthTTL / 12
)

type ProtocolRunner struct {
	repository        protocolRunnerRepository
	observer          *ProtocolObserver
	probeClient       *protocolprobe.Client
	xrayBinary        string
	echoPort          int
	interval          time.Duration
	repositoryTimeout time.Duration
	logger            *slog.Logger
	wake              chan struct{}
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

type protocolPublishResult struct {
	rejected []error
	err      error
}

type protocolRefreshResult struct {
	inputs []ProbeState
	err    error
}

func NewProtocolRunner(repository protocolRunnerRepository, xrayBinary string, echoPort int, interval time.Duration, logger *slog.Logger) *ProtocolRunner {
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 || interval > endpoints.DefaultHealthTTL/3 {
		interval = 15 * time.Second
	}
	client := protocolprobe.NewClient(xrayBinary)
	observer := NewProtocolObserver(repository, time.Now)
	observer.check = client.Check
	return &ProtocolRunner{repository: repository, observer: observer, probeClient: client, xrayBinary: xrayBinary, echoPort: echoPort, interval: interval, repositoryTimeout: protocolRepositoryTimeout, logger: logger, wake: make(chan struct{}, 1)}
}

func (r *ProtocolRunner) repositoryCallTimeout() time.Duration {
	if r.repositoryTimeout <= 0 {
		return protocolRepositoryTimeout
	}
	return r.repositoryTimeout
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
	defer r.probeClient.Close()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	jobs := make(chan probeJob)
	results := make(chan probeResult, protocolProbeWorkers)
	publishRequests := make(chan []probeResult, protocolPublishQueueSize)
	publishResults := make(chan protocolPublishResult, protocolPublishQueueSize)
	var publisher sync.WaitGroup
	publisher.Add(1)
	go func() {
		defer publisher.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case batch := <-publishRequests:
				observations := make([]ProtocolObservation, len(batch))
				for i, result := range batch {
					observations[i] = result.observation
				}
				publishContext, cancel := context.WithTimeout(ctx, r.repositoryCallTimeout())
				rejected, err := r.repository.PublishProtocolHealthBatch(publishContext, observations)
				cancel()
				select {
				case publishResults <- protocolPublishResult{rejected: rejected, err: err}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
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
					spec := r.probeSpec(job.input, job.member)
					observation, err := r.observer.observeState(ctx, job.input.State, job.input.Config, job.member.NodeID, spec)
					select {
					case results <- probeResult{job, observation, err}:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}
	refreshRequests := make(chan struct{}, 1)
	refreshResults := make(chan protocolRefreshResult, 1)
	var refreshWorker sync.WaitGroup
	refreshWorker.Add(1)
	go func() {
		defer refreshWorker.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-refreshRequests:
				refreshContext, cancel := context.WithTimeout(ctx, r.repositoryCallTimeout())
				inputs, err := r.loadRefresh(refreshContext)
				cancel()
				if err == nil {
					prepareContext, cancel := context.WithTimeout(ctx, 10*time.Second)
					prepareErr := r.prepareProbeClient(prepareContext, inputs)
					cancel()
					if prepareErr != nil && ctx.Err() == nil {
						r.logger.Warn("VLESS shared probe client refresh failed", "error", prepareErr)
					}
				}
				select {
				case refreshResults <- protocolRefreshResult{inputs: inputs, err: err}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	defer func() { cancel(); workers.Wait(); publisher.Wait(); refreshWorker.Wait() }()
	targets := make(map[string]*scheduledProbe)
	requestRefresh := func() {
		select {
		case refreshRequests <- struct{}{}:
		default:
		}
	}
	requestRefresh()
	refreshTicker := time.NewTicker(r.interval)
	defer refreshTicker.Stop()
	flushTicker := time.NewTicker(time.Second)
	defer flushTicker.Stop()
	pending := make([]probeResult, 0, 32)
	var retryBatch []probeResult
	var retryAt time.Time
	retryDelay := time.Second
	var publishInFlight []probeResult
	publishBusy := false
	var dispatchSequence uint64
	var priorityDispatches int
	flush := func() {
		if publishBusy {
			return
		}
		now := time.Now()
		// A failed publication must not keep an expired observation ahead of
		// newer probe results forever. The repository rejects expired leases,
		// so retaining them only delays the next usable health update.
		if len(retryBatch) > 0 {
			retryBatch = dropExpiredProtocolResults(retryBatch, now)
			if len(retryBatch) == 0 {
				retryAt = time.Time{}
				retryDelay = time.Second
			}
		}
		if !retryAt.IsZero() && now.Before(retryAt) {
			return
		}
		// A transaction-level failure means PostgreSQL did not commit any item.
		// Keep that exact batch for retry instead of dropping successful probes
		// and waiting for the next lease cycle to make them eligible again.
		batch := retryBatch
		if len(batch) == 0 {
			batch = pending
			pending = nil
		}
		if len(batch) == 0 {
			return
		}
		select {
		case publishRequests <- batch:
			publishInFlight = batch
			publishBusy = true
			if len(retryBatch) > 0 {
				retryBatch = nil
			} else {
				pending = nil
			}
		default:
			// The bounded publisher is busy; leave the batch in its source
			// buffer so a slow database cannot cause an observation drop.
		}
	}
	for {
		// Select the earliest due probe; prioritize one candidate from each pool
		// before probing its remaining members, including newly deployed rules.
		next := nextFairProbe(targets, time.Now(), priorityDispatches)
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
			requestRefresh()
		case <-refreshTicker.C:
			requestRefresh()
		case refreshed := <-refreshResults:
			if refreshed.err != nil {
				if !errors.Is(refreshed.err, context.Canceled) {
					r.logger.Warn("VLESS protocol scheduling refresh failed", "error", refreshed.err)
				}
				continue
			}
			r.applyRefresh(refreshed.inputs, targets)
		case <-flushTicker.C:
			flush()
		case send <- job:
			dispatchSequence = job.attempt
			next.inFlight = job.attempt
			if next.priority == 0 {
				if priorityDispatches < protocolPriorityBurst {
					priorityDispatches++
				}
			} else {
				priorityDispatches = 0
			}
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
				// Renew halfway through the 90-second lease. This leaves a full
				// failure window for a fresh challenge and durable publication.
				target.due = time.Now().Add(endpoints.DefaultHealthTTL / 2)
			}
			if result.err != nil {
				if target != nil {
					target.due = time.Now().Add(r.interval)
				}
				r.logger.Debug("VLESS protocol observation failed", "pool_id", result.job.input.State.Pool.ID, "node_id", result.job.member.NodeID, "error", result.err)
			} else {
				if len(pending) < protocolPendingLimit {
					pending = append(pending, result)
				} else if target != nil {
					// A database outage must not grow memory without bound. The
					// target remains due for a fresh probe, which will produce a
					// newer observation after publishing recovers.
					target.due = time.Now().Add(r.interval)
				}
				if len(pending) >= 32 {
					flush()
				}
			}
		case published := <-publishResults:
			batch := publishInFlight
			publishInFlight = nil
			publishBusy = false
			// The repository contract is positional: one rejection slot must be
			// returned for every observation, including successful observations.
			// Treat a malformed response as a transaction-level failure. Assuming
			// missing slots succeeded can silently drop a lease renewal and leave
			// a client routed to an expired backend.
			if published.err == nil && len(published.rejected) != len(batch) {
				published.err = fmt.Errorf("protocol health publisher returned %d rejection results for %d observations", len(published.rejected), len(batch))
			}
			if published.err != nil {
				r.logger.Warn("VLESS protocol health batch failed", "count", len(batch), "error", published.err)
				retryBatch = batch
				retryAt = nextHealthRetryAt(batch, time.Now(), retryDelay)
				if retryDelay < protocolRetryDelayCap {
					retryDelay *= 2
					if retryDelay > protocolRetryDelayCap {
						retryDelay = protocolRetryDelayCap
					}
				}
			} else {
				retryBatch = nil
				retryAt = time.Time{}
				retryDelay = time.Second
				for i, result := range batch {
					var failure error
					if i < len(published.rejected) {
						failure = published.rejected[i]
					}
					if failure != nil {
						if target := targets[result.job.key]; target != nil && sameProbe(target.job, result.job) {
							target.due = time.Now().Add(r.interval)
						}
					}
				}
				r.logger.Debug("VLESS protocol health batch published", "count", len(batch))
			}
			flush()
		}
	}
}

func (r *ProtocolRunner) probeSpec(input ProbeState, member endpoints.EndpointPoolMember) protocolprobe.Spec {
	rule := input.State.Rule
	return protocolprobe.Spec{XrayBinary: r.xrayBinary, DialHost: member.DialHost, DialPort: rule.ListenPort,
		UUID: input.Config.UUID, Flow: rule.EffectiveVLESSFlow(), FlowExplicit: true,
		ServerName: rule.RealityServerName, PublicKey: rule.RealityPublicKey, ShortID: rule.RealityShortID,
		EchoHost: "127.0.0.1", EchoPort: input.Config.EchoPort}
}

func (r *ProtocolRunner) prepareProbeClient(ctx context.Context, inputs []ProbeState) error {
	var specs []protocolprobe.Spec
	for _, input := range inputs {
		state := input.State
		if state.Rule.Paused || state.Rule.IngressReadiness() != forwarding.IngressReady ||
			(state.Rule.Status != forwarding.StatusPendingActivation && state.Rule.Status != forwarding.StatusActive) {
			continue
		}
		for _, member := range state.Members {
			if member.State == endpoints.CandidateEligible && member.Weight > 0 &&
				deploymentReady(state.Deployments[member.NodeID], state.Rule, member.NodeID) {
				specs = append(specs, r.probeSpec(input, member))
			}
		}
	}
	return r.probeClient.Prepare(ctx, specs)
}

func dropExpiredProtocolResults(batch []probeResult, now time.Time) []probeResult {
	if len(batch) == 0 {
		return nil
	}
	kept := batch[:0]
	for _, result := range batch {
		lease := result.observation.LeaseExpiresAt
		if !lease.IsZero() && !lease.After(now) {
			continue
		}
		kept = append(kept, result)
	}
	return kept
}

// nextHealthRetryAt keeps a failed batch inside the current lease window. The
// publisher may still be unavailable for longer than the lease, but it should
// not be the scheduler's backoff policy that causes the expiry.
func nextHealthRetryAt(batch []probeResult, now time.Time, delay time.Duration) time.Time {
	if delay <= 0 {
		delay = time.Second
	}
	if delay > protocolRetryDelayCap {
		delay = protocolRetryDelayCap
	}
	next := now.Add(delay)
	for _, result := range batch {
		lease := result.observation.LeaseExpiresAt
		if lease.IsZero() {
			continue
		}
		// Leave one retry interval of safety for the database transaction and
		// network scheduling while the lease is still valid. Once the lease has
		// expired, retain the normal backoff instead of returning now and
		// creating a busy retry loop.
		deadline := lease.Add(-protocolRetryDelayCap)
		if deadline.After(now) && deadline.Before(next) {
			next = deadline
		}
	}
	return next
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

// Keep recovery and lease renewal preferred, while reserving a dispatch after
// each bounded priority burst for the oldest due remaining candidate.
// Continuous failures must not starve the rest of a group's nodes forever.
func nextFairProbe(targets map[string]*scheduledProbe, now time.Time, priorityDispatches int) *scheduledProbe {
	if priorityDispatches >= protocolPriorityBurst {
		var oldest *scheduledProbe
		for _, target := range targets {
			if target.priority == 0 || target.inFlight != 0 || target.due.After(now) {
				continue
			}
			if oldest == nil || target.due.Before(oldest.due) || target.due.Equal(oldest.due) && target.job.key < oldest.job.key {
				oldest = target
			}
		}
		if oldest != nil {
			return oldest
		}
	}
	return nextProbe(targets, now)
}

func sameProbe(a, b probeJob) bool {
	x, y := a.input.State, b.input.State
	ex, ey := x.Deployments[a.member.NodeID].Status, y.Deployments[b.member.NodeID].Status
	// The scheduler key normally contains pool/node IDs, but a refresh can
	// replace a target while an old job is still in flight. Compare every
	// challenge input explicitly so an old completion cannot be associated with
	// a different pool, node, listener, or Reality target if IDs are reused.
	return x.Pool.ID == y.Pool.ID && x.Rule.ID == y.Rule.ID &&
		a.member.PoolID == b.member.PoolID && a.member.GroupID == b.member.GroupID &&
		a.member.NodeID == b.member.NodeID && a.member.DialHost == b.member.DialHost &&
		x.Rule.EffectiveRuntimeRevision() == y.Rule.EffectiveRuntimeRevision() && x.Rule.ListenPort == y.Rule.ListenPort &&
		x.Rule.EffectiveVLESSFlow() == y.Rule.EffectiveVLESSFlow() &&
		x.Rule.RealityServerName == y.Rule.RealityServerName &&
		x.Rule.RealityPublicKey == y.Rule.RealityPublicKey &&
		x.Rule.RealityShortID == y.Rule.RealityShortID &&
		ex.RuleID == ey.RuleID && ex.NodeID == ey.NodeID &&
		sameProbeDeployment(ex, ey) &&
		a.input.Config == b.input.Config
}

// A new unrelated fragment does not change this challenge target or its due
// time. Legacy receipts retain full-bundle matching until a real probe emits
// the stable fragment identity. Do not let malformed identities compare equal.
func sameProbeDeployment(a, b deploymentreceipts.Status) bool {
	if a.RuleConfigSHA256 != "" || b.RuleConfigSHA256 != "" {
		return len(a.RuleConfigSHA256) == 64 && a.RuleConfigSHA256 == b.RuleConfigSHA256
	}
	return a.NodeConfigGeneration == b.NodeConfigGeneration && a.ConfigSHA256 == b.ConfigSHA256
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
			proof.RuleID != state.Rule.ID || proof.NodeID != member.NodeID || proof.RuleRevision != state.Rule.EffectiveRuntimeRevision() ||
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

func (r *ProtocolRunner) loadRefresh(ctx context.Context) ([]ProbeState, error) {
	inputs, err := r.repository.ProtocolProbeStates(ctx)
	if err != nil {
		return nil, err
	}
	ready := make([]ProbeState, 0, len(inputs))
	var initializeRuleID string
	missing := make(map[string]ProtocolProbeConfig)
	for _, input := range inputs {
		state := input.State
		if state.Rule.Paused || state.Rule.IngressReadiness() != forwarding.IngressReady || (state.Rule.Status != forwarding.StatusPendingActivation && state.Rule.Status != forwarding.StatusActive) {
			continue
		}
		if input.Config.UUID == "" {
			if len(missing) < 512 {
				missing[state.Rule.ID] = ProtocolProbeConfig{}
			}
			if initializeRuleID == "" {
				initializeRuleID = state.Rule.ID
			}
			continue
		}
		ready = append(ready, input)
	}
	// Initializing each missing identity recompiles the complete node bundle.
	// Bound that work to one write per refresh, and preserve already configured
	// probes even when that write fails. A large import must not starve every
	// existing health lease until all new identities have been initialized.
	if initializeRuleID != "" {
		if batch, ok := r.repository.(interface {
			ConfigureProtocolProbes(context.Context, map[string]ProtocolProbeConfig) error
		}); ok {
			for id := range missing {
				config, err := NewProtocolProbeConfig(r.echoPort)
				if err != nil {
					return nil, err
				}
				missing[id] = config
			}
			if err := batch.ConfigureProtocolProbes(ctx, missing); err != nil {
				r.logger.Warn("VLESS protocol identity batch initialization deferred", "error", err)
			}
			return ready, nil
		}
		config, err := NewProtocolProbeConfig(r.echoPort)
		if err != nil {
			return nil, err
		}
		if err := r.repository.ConfigureProtocolProbe(ctx, initializeRuleID, config); err != nil && !errors.Is(err, faults.ErrConflict) {
			r.logger.Warn("VLESS protocol identity initialization deferred", "error", err)
		}
		// The newly configured identity is deliberately absent from ready.
		// Its new deployment must be acknowledged before it can be challenged.
	}
	return ready, nil
}

func (r *ProtocolRunner) refresh(ctx context.Context, targets map[string]*scheduledProbe) error {
	inputs, err := r.loadRefresh(ctx)
	if err != nil {
		return err
	}
	r.applyRefresh(inputs, targets)
	return nil
}

func (r *ProtocolRunner) applyRefresh(inputs []ProbeState, targets map[string]*scheduledProbe) {
	seen := make(map[string]bool)
	now := time.Now()
	for _, input := range inputs {
		state := input.State
		if state.Rule.Paused || state.Rule.IngressReadiness() != forwarding.IngressReady || (state.Rule.Status != forwarding.StatusPendingActivation && state.Rule.Status != forwarding.StatusActive) {
			continue
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
				renew := health.VerifiedAt.Add(endpoints.DefaultHealthTTL / 2)
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
