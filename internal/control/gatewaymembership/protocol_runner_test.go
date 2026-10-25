package gatewaymembership

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/protocolprobe"
)

type scanRepository struct {
	protocolRunnerRepository
	scanned chan struct{}
}

type initializationRepository struct {
	*schedulingRepository
	configured []string
	err        error
}

func (s *initializationRepository) ConfigureProtocolProbe(_ context.Context, id string, _ ProtocolProbeConfig) error {
	s.configured = append(s.configured, id)
	return s.err
}

func TestProbeInitializationDoesNotStarveConfiguredRules(t *testing.T) {
	for _, initializationErr := range []error{nil, context.DeadlineExceeded} {
		t.Run(fmt.Sprint(initializationErr), func(t *testing.T) {
			missingA := schedulingInput("new-a", 1)
			missingA.Config = ProtocolProbeConfig{}
			missingB := schedulingInput("new-b", 1)
			missingB.Config = ProtocolProbeConfig{}
			repo := &initializationRepository{schedulingRepository: &schedulingRepository{states: []ProbeState{missingA, schedulingInput("existing", 1), missingB}}, err: initializationErr}
			runner := NewProtocolRunner(repo, "unused", 39000, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
			ready, err := runner.loadRefresh(context.Background())
			if err != nil || len(ready) != 1 || ready[0].State.Rule.ID != "existing" {
				t.Fatalf("configured rule lost during initialization: ready=%v err=%v", ready, err)
			}
			if len(repo.configured) != 1 || repo.configured[0] != "new-a" {
				t.Fatalf("initialization must be bounded to one write: %v", repo.configured)
			}
		})
	}
}

type schedulingRepository struct {
	protocolRunnerRepository
	mu              sync.Mutex
	states          []ProbeState
	published       chan []ProtocolObservation
	reads           int
	publishFailures int
	publishCalls    int
	malformedResult bool
	blockReads      <-chan struct{}
	readStarted     chan struct{}
}

type timeoutPublishRepository struct {
	*schedulingRepository
	started chan struct{}
	calls   atomic.Int32
}

func (s *timeoutPublishRepository) PublishProtocolHealthBatch(ctx context.Context, items []ProtocolObservation) ([]error, error) {
	if s.calls.Add(1) == 1 {
		close(s.started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return s.schedulingRepository.PublishProtocolHealthBatch(ctx, items)
}

type timeoutRefreshRepository struct {
	*schedulingRepository
	started chan struct{}
	calls   atomic.Int32
}

func (s *timeoutRefreshRepository) ProtocolProbeStates(ctx context.Context) ([]ProbeState, error) {
	if s.calls.Add(1) == 1 {
		close(s.started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return s.schedulingRepository.ProtocolProbeStates(ctx)
}

func (s *schedulingRepository) ProtocolProbeStates(ctx context.Context) ([]ProbeState, error) {
	s.mu.Lock()
	s.reads++
	blockReads := s.blockReads
	readStarted := s.readStarted
	states := append([]ProbeState(nil), s.states...)
	s.mu.Unlock()
	if readStarted != nil {
		select {
		case readStarted <- struct{}{}:
		default:
		}
	}
	if blockReads != nil {
		select {
		case <-blockReads:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return states, nil
}
func (s *schedulingRepository) PublishProtocolHealthBatch(_ context.Context, items []ProtocolObservation) ([]error, error) {
	s.mu.Lock()
	s.publishCalls++
	fail := s.publishFailures > 0
	if fail {
		s.publishFailures--
	}
	malformed := s.malformedResult
	if malformed {
		s.malformedResult = false
	}
	s.mu.Unlock()
	if fail {
		return nil, errors.New("transient publish failure")
	}
	if malformed {
		if len(items) == 0 {
			return []error{}, nil
		}
		return make([]error, len(items)-1), nil
	}
	if s.published != nil {
		s.published <- append([]ProtocolObservation(nil), items...)
	}
	return make([]error, len(items)), nil
}
func schedulingInput(id string, members int) ProbeState {
	state := stateWithVerifiedEvidence()
	state.Pool.ID = id
	state.Pool.RuleID = id
	state.Rule.ID = id
	state.Members = nil
	for i := 0; i < members; i++ {
		member := endpoints.EndpointPoolMember{PoolID: id, GroupID: state.Pool.GroupID, NodeID: fmt.Sprintf("%s-%d", id, i), DialHost: fmt.Sprintf("%s-%d.example.test", id, i), State: endpoints.CandidateEligible, Weight: 1}
		state.Members = append(state.Members, member)
		evidence := state.Deployments["node-1"]
		evidence.Status.RuleID = id
		evidence.Status.NodeID = member.NodeID
		state.Deployments[member.NodeID] = evidence
	}
	state.ProtocolHealth = nil
	return ProbeState{State: state, Config: ProtocolProbeConfig{UUID: "probe-only", EchoPort: 39000}}
}

func TestExpiredProvenCandidateDoesNotFallBehindUnprovenBacklog(t *testing.T) {
	for _, changedDeployment := range []bool{false, true} {
		t.Run(fmt.Sprintf("changed-deployment-%v", changedDeployment), func(t *testing.T) {
			input := schedulingInput("recover", 2)
			member := input.State.Members[1] // The first member has never passed.
			evidence := input.State.Deployments[member.NodeID].Status
			now := time.Now().UTC()
			proof := ProtocolObservation{
				RuleID: input.State.Rule.ID, NodeID: member.NodeID, RuleRevision: input.State.Rule.Revision,
				NodeConfigGeneration: evidence.NodeConfigGeneration, ConfigSHA256: evidence.ConfigSHA256,
				Protocol: input.State.Rule.EffectiveIngressProtocol(), DialHost: member.DialHost,
				VerifiedAt: now.Add(-2 * time.Minute), LeaseExpiresAt: now.Add(-30 * time.Second),
			}
			if changedDeployment {
				proof.NodeConfigGeneration--
			}
			input.State.ProtocolHealth = map[string]ProtocolObservation{member.NodeID: proof}
			repo := &schedulingRepository{states: []ProbeState{input, schedulingInput("other", 1)}}
			runner := NewProtocolRunner(repo, "unused", 39000, 15*time.Second, nil)
			targets := make(map[string]*scheduledProbe)
			if err := runner.refresh(context.Background(), targets); err != nil {
				t.Fatal(err)
			}
			targets["recover:recover-0"].due = now.Add(time.Minute)
			targets["recover:recover-1"].due = now.Add(-time.Minute)
			targets["other:other-0"].due = now.Add(-time.Second)
			want := "recover:recover-1"
			if changedDeployment {
				want = "other:other-0"
			}
			if next := nextProbe(targets, now); next == nil || next.job.key != want {
				t.Fatalf("previously proven recovery was delayed by unrelated probes: want=%s", want)
			}
			// Scheduling an expired proof sooner must not authorize it for use.
			if ReadyCandidateCount(input.State, now) != 0 {
				t.Fatal("expired or replaced proof granted readiness")
			}
		})
	}
}

func TestBundleChangeRecoversRecentlySuccessfulAddressBeforeUnprovenNode(t *testing.T) {
	input := schedulingInput("existing-rule", 3)
	member := input.State.Members[1]
	evidence := input.State.Deployments[member.NodeID].Status
	now := time.Now().UTC()
	proof := ProtocolObservation{
		RuleID: input.State.Rule.ID, NodeID: member.NodeID, RuleRevision: input.State.Rule.Revision,
		NodeConfigGeneration: evidence.NodeConfigGeneration - 1, ConfigSHA256: evidence.ConfigSHA256,
		Protocol: input.State.Rule.EffectiveIngressProtocol(), DialHost: member.DialHost,
		VerifiedAt: now.Add(-10 * time.Second), LeaseExpiresAt: now.Add(80 * time.Second),
	}
	input.State.ProtocolHealth = map[string]ProtocolObservation{member.NodeID: proof}
	repo := &schedulingRepository{states: []ProbeState{input, schedulingInput("new-rule", 1)}}
	runner := NewProtocolRunner(repo, "unused", 39000, 15*time.Second, nil)
	targets := make(map[string]*scheduledProbe)
	if err := runner.refresh(context.Background(), targets); err != nil {
		t.Fatal(err)
	}
	if targets["existing-rule:existing-rule-1"].priority != 0 || targets["existing-rule:existing-rule-0"].priority != 1 ||
		targets["existing-rule:existing-rule-2"].priority != 1 || targets["new-rule:new-rule-0"].priority != 0 {
		t.Fatal("bundle recovery did not prioritize one proven address per rule")
	}
	if ReadyCandidateCount(input.State, now) != 0 {
		t.Fatal("previous bundle's proof authorized the new bundle")
	}
	proof.DialHost = "replaced.example.test"
	input.State.ProtocolHealth[member.NodeID] = proof
	repo.states[0] = input
	if err := runner.refresh(context.Background(), targets); err != nil {
		t.Fatal(err)
	}
	if targets["existing-rule:existing-rule-1"].priority != 1 || targets["existing-rule:existing-rule-0"].priority != 0 {
		t.Fatal("a changed address inherited prior protocol success")
	}
}

func TestSameProbeRejectsChangedChallengeTarget(t *testing.T) {
	input := schedulingInput("same-probe", 1)
	member := input.State.Members[0]
	base := probeJob{key: "same-probe:" + member.NodeID, input: input, member: member, attempt: 1}

	mutations := map[string]func(*probeJob){
		"pool":     func(job *probeJob) { job.input.State.Pool.ID = "other-pool" },
		"rule":     func(job *probeJob) { job.input.State.Rule.ID = "other-rule" },
		"node":     func(job *probeJob) { job.member.NodeID = "other-node" },
		"listener": func(job *probeJob) { job.input.State.Rule.ListenPort++ },
		"flow": func(job *probeJob) {
			job.input.State.Rule.VLESSFlow = ""
			job.input.State.Rule.VLESSFlowExplicit = true
		},
		"reality server name": func(job *probeJob) { job.input.State.Rule.RealityServerName = "other.example" },
		"reality public key":  func(job *probeJob) { job.input.State.Rule.RealityPublicKey = "other-key" },
		"reality short id":    func(job *probeJob) { job.input.State.Rule.RealityShortID = "other-short-id" },
		"dial host":           func(job *probeJob) { job.member.DialHost = "other.example" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			if sameProbe(base, changed) {
				t.Fatalf("sameProbe accepted changed %s", name)
			}
		})
	}
	if !sameProbe(base, base) {
		t.Fatal("sameProbe rejected an identical job")
	}
}

func TestProtocolSchedulerIsFairBoundedAndBatchesWithoutPerNodeReads(t *testing.T) {
	repo := &schedulingRepository{published: make(chan []ProtocolObservation, 100)}
	for i := 0; i < 36; i++ {
		repo.states = append(repo.states, schedulingInput(fmt.Sprintf("pool-%02d", i), 7))
	}
	runner := NewProtocolRunner(repo, "unused", 39000, 15*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	started := make(chan string, 300)
	var live, maximum atomic.Int32
	runner.observer.check = func(ctx context.Context, spec protocolprobe.Spec) (time.Time, error) {
		n := live.Add(1)
		defer live.Add(-1)
		for old := maximum.Load(); n > old; old = maximum.Load() {
			if maximum.CompareAndSwap(old, n) {
				break
			}
		}
		started <- spec.DialHost
		select {
		case <-ctx.Done():
			return time.Time{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
			return time.Now().UTC(), nil
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	defer func() { cancel(); <-done }()
	pools := map[string]bool{}
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	for len(pools) < 36 {
		select {
		case host := <-started:
			pools[host[:len("pool-00")]] = true
		case <-deadline.C:
			t.Fatal("a large pool blocked other rules")
		}
	}
	total := 0
	for total < 252 {
		select {
		case batch := <-repo.published:
			total += len(batch)
		case <-deadline.C:
			t.Fatal("healthy rules waited for serial pool scans")
		}
	}
	if maximum.Load() > protocolProbeWorkers || maximum.Load() < 2 {
		t.Fatalf("unbounded or serial probes: %d", maximum.Load())
	}
	repo.mu.Lock()
	reads := repo.reads
	repo.mu.Unlock()
	if reads != 1 {
		t.Fatalf("per-node snapshot reads: %d", reads)
	}
}

func TestProtocolSchedulerContinuousRecoveryFailuresDoNotStarveOtherMembers(t *testing.T) {
	repo := &schedulingRepository{published: make(chan []ProtocolObservation, 100)}
	for i := 0; i < 32; i++ {
		repo.states = append(repo.states, schedulingInput(fmt.Sprintf("pool-%02d", i), 2))
	}
	runner := NewProtocolRunner(repo, "unused", 39000, time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	checked := make(chan string, 2048)
	runner.observer.check = func(ctx context.Context, spec protocolprobe.Spec) (time.Time, error) {
		checked <- spec.DialHost
		select {
		case <-ctx.Done():
			return time.Time{}, ctx.Err()
		case <-time.After(5 * time.Millisecond):
			return time.Time{}, errors.New("isolated simulated failure")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	defer func() { cancel(); <-done }()
	seen := make(map[string]bool)
	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
	for len(seen) < 32 {
		select {
		case host := <-checked:
			if strings.HasSuffix(host, "-1.example.test") {
				seen[host] = true
			}
		case <-timeout.C:
			t.Fatalf("continuous priority failures starved other members: checked=%d/32", len(seen))
		}
	}
}

func TestProtocolSchedulerContinuesProbingWhileRefreshReadIsBlocked(t *testing.T) {
	input := schedulingInput("blocked-refresh", 2)
	now := time.Now().UTC()
	member := input.State.Members[1]
	receipt := input.State.Deployments[member.NodeID].Status
	input.State.ProtocolHealth = map[string]ProtocolObservation{member.NodeID: {
		RuleID:               input.State.Rule.ID,
		NodeID:               member.NodeID,
		RuleRevision:         input.State.Rule.Revision,
		NodeConfigGeneration: receipt.NodeConfigGeneration,
		ConfigSHA256:         receipt.ConfigSHA256,
		Protocol:             input.State.Rule.EffectiveIngressProtocol(),
		DialHost:             member.DialHost,
		VerifiedAt:           now,
		LeaseExpiresAt:       now.Add(time.Minute),
	}}
	releaseFirst := make(chan struct{})
	firstStarted := make(chan struct{})
	blockReads := make(chan struct{})
	readStarted := make(chan struct{}, 2)
	repo := &schedulingRepository{states: []ProbeState{{State: input.State, Config: input.Config}}, readStarted: readStarted}
	runner := NewProtocolRunner(repo, "unused", 39000, 10*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var checks atomic.Int32
	runner.observer.check = func(ctx context.Context, _ protocolprobe.Spec) (time.Time, error) {
		if checks.Add(1) == 1 {
			close(firstStarted)
			select {
			case <-releaseFirst:
			case <-ctx.Done():
				return time.Time{}, ctx.Err()
			}
			runner.NotifyDeployment("refresh-while-probing")
			return time.Time{}, errors.New("simulated probe failure")
		}
		return time.Time{}, errors.New("simulated probe failure")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	defer func() {
		cancel()
		<-done
	}()
	select {
	case <-readStarted:
	case <-time.After(time.Second):
		t.Fatal("initial refresh did not start")
	}
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("initial protocol probe did not start")
	}
	repo.mu.Lock()
	repo.blockReads = blockReads
	repo.mu.Unlock()
	close(releaseFirst)
	select {
	case <-readStarted:
	case <-time.After(time.Second):
		t.Fatal("refresh worker did not reach the blocked read")
	}
	deadline := time.After(time.Second)
	for checks.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("probe dispatch stopped while refresh read was blocked: checks=%d", checks.Load())
		case <-time.After(time.Millisecond):
		}
	}
}

func TestProtocolSchedulerRetriesHealthBatchAfterTransientPublishFailure(t *testing.T) {
	repo := &schedulingRepository{
		states:          []ProbeState{schedulingInput("retry-rule", 1)},
		published:       make(chan []ProtocolObservation, 2),
		publishFailures: 1,
	}
	runner := NewProtocolRunner(repo, "unused", 39000, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	started := make(chan struct{}, 1)
	runner.observer.check = func(context.Context, protocolprobe.Spec) (time.Time, error) {
		started <- struct{}{}
		return time.Now().UTC(), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	defer func() {
		cancel()
		<-done
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("protocol challenge did not start")
	}
	select {
	case batch := <-repo.published:
		if len(batch) != 1 || batch[0].RuleID != "retry-rule" {
			t.Fatalf("unexpected retried health batch: %#v", batch)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("failed health batch was dropped instead of retried")
	}
	repo.mu.Lock()
	calls := repo.publishCalls
	repo.mu.Unlock()
	if calls < 2 {
		t.Fatalf("health batch was not retried after transient failure: calls=%d", calls)
	}
}

func TestProtocolSchedulerBoundsRepositoryPublishAndRetriesAfterTimeout(t *testing.T) {
	base := &schedulingRepository{
		states:    []ProbeState{schedulingInput("publish-timeout", 1)},
		published: make(chan []ProtocolObservation, 2),
	}
	repo := &timeoutPublishRepository{schedulingRepository: base, started: make(chan struct{})}
	runner := NewProtocolRunner(repo, "unused", 39000, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	runner.repositoryTimeout = 30 * time.Millisecond
	runner.observer.check = func(context.Context, protocolprobe.Spec) (time.Time, error) {
		return time.Now().UTC(), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case <-repo.started:
	case <-time.After(3 * time.Second):
		t.Fatal("health publication did not start")
	}
	select {
	case batch := <-base.published:
		if len(batch) != 1 || batch[0].RuleID != "publish-timeout" {
			t.Fatalf("unexpected retried health batch: %#v", batch)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed-out publisher did not release its worker for retry")
	}
	if calls := repo.calls.Load(); calls < 2 {
		t.Fatalf("health publisher was not retried after timeout: calls=%d", calls)
	}
}

func TestProtocolSchedulerBoundsRepositoryRefreshAndResumesProbing(t *testing.T) {
	base := &schedulingRepository{states: []ProbeState{schedulingInput("refresh-timeout", 1)}}
	repo := &timeoutRefreshRepository{schedulingRepository: base, started: make(chan struct{})}
	runner := NewProtocolRunner(repo, "unused", 39000, 20*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	runner.repositoryTimeout = 30 * time.Millisecond
	started := make(chan struct{}, 1)
	runner.observer.check = func(context.Context, protocolprobe.Spec) (time.Time, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		return time.Now().UTC(), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case <-repo.started:
	case <-time.After(time.Second):
		t.Fatal("state refresh did not start")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("protocol probing did not resume after refresh timeout")
	}
	if calls := repo.calls.Load(); calls < 2 {
		t.Fatalf("refresh worker did not retry after timeout: calls=%d", calls)
	}
}

func TestProtocolSchedulerRetriesMalformedHealthBatchResult(t *testing.T) {
	repo := &schedulingRepository{
		states:          []ProbeState{schedulingInput("malformed-result", 1)},
		published:       make(chan []ProtocolObservation, 2),
		malformedResult: true,
	}
	runner := NewProtocolRunner(repo, "unused", 39000, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	runner.observer.check = func(context.Context, protocolprobe.Spec) (time.Time, error) {
		return time.Now().UTC(), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	defer func() {
		cancel()
		<-done
	}()
	select {
	case batch := <-repo.published:
		if len(batch) != 1 || batch[0].RuleID != "malformed-result" {
			t.Fatalf("unexpected retried health batch: %#v", batch)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("malformed health batch result was treated as success")
	}
	repo.mu.Lock()
	calls := repo.publishCalls
	repo.mu.Unlock()
	if calls < 2 {
		t.Fatalf("malformed health batch was not retried: calls=%d", calls)
	}
}

func TestHealthRetryStaysBeforeObservationLease(t *testing.T) {
	now := time.Now().UTC()
	batch := []probeResult{{observation: ProtocolObservation{
		LeaseExpiresAt: now.Add(20 * time.Second),
	}}}
	next := nextHealthRetryAt(batch, now, 30*time.Second)
	if !next.After(now) {
		t.Fatal("retry was scheduled immediately despite remaining lease time")
	}
	if !next.Before(batch[0].observation.LeaseExpiresAt) {
		t.Fatalf("retry=%s was scheduled at or after lease=%s", next, batch[0].observation.LeaseExpiresAt)
	}
	if delay := next.Sub(now); delay > protocolRetryDelayCap {
		t.Fatalf("retry delay=%s exceeded cap=%s", delay, protocolRetryDelayCap)
	}
}

func TestHealthRetryUsesDelayWhenLeaseHasEnoughTime(t *testing.T) {
	now := time.Now().UTC()
	batch := []probeResult{{observation: ProtocolObservation{
		LeaseExpiresAt: now.Add(2 * time.Minute),
	}}}
	next := nextHealthRetryAt(batch, now, 2*time.Second)
	if delay := next.Sub(now); delay < 2*time.Second || delay > 2100*time.Millisecond {
		t.Fatalf("retry delay=%s did not preserve requested backoff", delay)
	}
}

func TestHealthRetryKeepsBackoffAfterLeaseExpires(t *testing.T) {
	now := time.Now().UTC()
	batch := []probeResult{{observation: ProtocolObservation{
		LeaseExpiresAt: now.Add(-time.Second),
	}}}
	next := nextHealthRetryAt(batch, now, 2*time.Second)
	if !next.After(now) {
		t.Fatal("expired lease caused an immediate retry")
	}
	if delay := next.Sub(now); delay < 2*time.Second || delay > 2100*time.Millisecond {
		t.Fatalf("retry delay=%s did not preserve backoff after expiry", delay)
	}
}

func TestDropExpiredProtocolResultsDoesNotBlockFreshResults(t *testing.T) {
	now := time.Now().UTC()
	old := probeResult{observation: ProtocolObservation{LeaseExpiresAt: now.Add(-time.Second)}}
	fresh := probeResult{observation: ProtocolObservation{LeaseExpiresAt: now.Add(time.Minute)}}
	batch := []probeResult{old, fresh}
	kept := dropExpiredProtocolResults(batch, now)
	if len(kept) != 1 || kept[0].observation.LeaseExpiresAt != fresh.observation.LeaseExpiresAt {
		t.Fatalf("expired retry results were not removed: %#v", kept)
	}
	if got := dropExpiredProtocolResults([]probeResult{old}, now); len(got) != 0 {
		t.Fatalf("all-expired retry batch was retained: %#v", got)
	}
}

func TestDropExpiredProtocolResultsKeepsZeroLeaseForRepositoryValidation(t *testing.T) {
	result := probeResult{observation: ProtocolObservation{}}
	if got := dropExpiredProtocolResults([]probeResult{result}, time.Now().UTC()); len(got) != 1 {
		t.Fatal("zero lease observation was dropped before repository validation")
	}
}

func TestProtocolSchedulerNewDeploymentSupersedesInFlightProof(t *testing.T) {
	repo := &schedulingRepository{states: []ProbeState{schedulingInput("new-rule", 1)}, published: make(chan []ProtocolObservation, 20)}
	runner := NewProtocolRunner(repo, "unused", 39000, 15*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var calls atomic.Int32
	runner.observer.check = func(ctx context.Context, _ protocolprobe.Spec) (time.Time, error) {
		started <- struct{}{}
		if calls.Add(1) == 1 {
			select {
			case <-release:
			case <-ctx.Done():
				return time.Time{}, ctx.Err()
			}
		}
		return time.Now().UTC(), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("initial challenge not started")
	}
	repo.mu.Lock()
	input := schedulingInput("new-rule", 1)
	input.State.Rule.Revision++
	evidence := input.State.Deployments[input.State.Members[0].NodeID]
	evidence.RuleRevision++
	evidence.Status.NodeConfigGeneration++
	input.State.Deployments[input.State.Members[0].NodeID] = evidence
	repo.states = []ProbeState{input}
	repo.mu.Unlock()
	runner.NotifyDeployment("node")
	deadline := time.Now().Add(time.Second)
	for {
		repo.mu.Lock()
		reads := repo.reads
		repo.mu.Unlock()
		if reads >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("deployment wake not read")
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("old challenge delayed the new generation")
	}
	select {
	case batch := <-repo.published:
		if len(batch) != 1 || batch[0].RuleRevision != input.State.Rule.Revision {
			t.Fatal("stale proof published")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("new generation proof not published")
	}
}

func TestNewRuleBypassesLargePoolBacklog(t *testing.T) {
	repo := &schedulingRepository{states: []ProbeState{schedulingInput("old-rule", protocolProbeWorkers*4)}, published: make(chan []ProtocolObservation, 20)}
	runner := NewProtocolRunner(repo, "unused", 39000, 15*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	started := make(chan string, 100)
	release := make(chan struct{})
	runner.observer.check = func(ctx context.Context, spec protocolprobe.Spec) (time.Time, error) {
		started <- spec.DialHost
		select {
		case <-release:
			return time.Now().UTC(), nil
		case <-ctx.Done():
			return time.Time{}, ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	defer func() { cancel(); <-done }()
	for i := 0; i < protocolProbeWorkers; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("workers did not start")
		}
	}
	repo.mu.Lock()
	repo.states = append(repo.states, schedulingInput("new-rule", 1))
	repo.mu.Unlock()
	runner.NotifyDeployment("new-node")
	deadline := time.Now().Add(time.Second)
	for {
		repo.mu.Lock()
		reads := repo.reads
		repo.mu.Unlock()
		if reads >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("new deployment not refreshed")
		}
		time.Sleep(time.Millisecond)
	}
	// Free one worker so the next dispatched job is observable. Releasing all
	// workers lets their check callbacks race, which does not preserve dispatch
	// order and can falsely report that a correctly prioritized job was delayed.
	select {
	case release <- struct{}{}:
	case <-time.After(time.Second):
		t.Fatal("blocked worker did not finish")
	}
	select {
	case host := <-started:
		if host != "new-rule-0.example.test" {
			t.Fatal("new rule waited behind unrelated remaining candidates")
		}
	case <-time.After(time.Second):
		t.Fatal("new rule was not scheduled")
	}
}

func (s scanRepository) ProtocolProbeStates(context.Context) ([]ProbeState, error) {
	s.scanned <- struct{}{}
	return nil, nil
}

func TestVerificationReceiptWakesProtocolRunnerWithoutWaitingInterval(t *testing.T) {
	repo := scanRepository{scanned: make(chan struct{}, 4)}
	runner := NewProtocolRunner(repo, "unused", 39000, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	select {
	case <-repo.scanned:
	case <-time.After(time.Second):
		t.Fatal("initial scan missing")
	}
	runner.NotifyDeployment("node")
	select {
	case <-repo.scanned:
	case <-time.After(time.Second):
		t.Fatal("verification waited for periodic scan")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runner did not stop")
	}
}
