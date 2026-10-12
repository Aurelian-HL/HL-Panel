package gatewaymembership

import (
	"context"
	"fmt"
	"io"
	"log/slog"
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

type schedulingRepository struct {
	protocolRunnerRepository
	mu        sync.Mutex
	states    []ProbeState
	published chan []ProtocolObservation
	reads     int
}

func (s *schedulingRepository) ProtocolProbeStates(context.Context) ([]ProbeState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	return append([]ProbeState(nil), s.states...), nil
}
func (s *schedulingRepository) PublishProtocolHealthBatch(_ context.Context, items []ProtocolObservation) ([]error, error) {
	s.published <- append([]ProtocolObservation(nil), items...)
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
	repo := &schedulingRepository{states: []ProbeState{schedulingInput("old-rule", 32)}, published: make(chan []ProtocolObservation, 20)}
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
