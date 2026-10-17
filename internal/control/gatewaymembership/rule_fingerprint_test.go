package gatewaymembership

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/endpoints"
)

func stateWithStableRuleEvidence() State {
	state := stateWithVerifiedEvidence()
	evidence := state.Deployments["node-1"]
	evidence.Status.RuleConfigSHA256 = strings.Repeat("a", 64)
	state.Deployments["node-1"] = evidence
	proof := state.ProtocolHealth["node-1"]
	proof.RuleConfigSHA256 = evidence.Status.RuleConfigSHA256
	state.ProtocolHealth["node-1"] = proof
	return state
}

func TestStableRuleProofSurvivesUnrelatedBundleWithoutRenewingLease(t *testing.T) {
	state := stateWithStableRuleEvidence()
	proof := state.ProtocolHealth["node-1"]
	evidence := state.Deployments["node-1"]
	evidence.Status.NodeConfigGeneration++
	evidence.Status.ConfigSHA256 = strings.Repeat("b", 64)
	state.Deployments["node-1"] = evidence
	service := NewService(&stateRepository{state})
	service.clock = func() time.Time { return proof.VerifiedAt.Add(time.Second) }
	snapshot, err := service.Snapshot(context.Background(), state.Pool.ID)
	if err != nil || len(snapshot.Endpoints) != 1 || !snapshot.Endpoints[0].HealthLeaseExpiresAt.Equal(proof.LeaseExpiresAt) {
		t.Fatal("bundle refresh lost or extended original rule lease", err)
	}
	if ReadyCandidateCount(state, proof.VerifiedAt.Add(time.Second)) != 1 {
		t.Fatal("unchanged fragment lost readiness")
	}
	if ReadyCandidateCount(state, proof.LeaseExpiresAt) != 0 {
		t.Fatal("bundle refresh authorized expired original lease")
	}
	if !proof.MatchesCurrentDeployment(state.Pool, state.Rule, state.Members[0], evidence, proof.VerifiedAt.Add(time.Second)) {
		t.Fatal("in-flight probe from identical prior fragment was rejected")
	}
	encoded, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	var restored ProtocolObservation
	if err = json.Unmarshal(encoded, &restored); err != nil || restored.RuleConfigSHA256 != proof.RuleConfigSHA256 || !restored.LeaseExpiresAt.Equal(proof.LeaseExpiresAt) {
		t.Fatal("fragment identity or lease lost in persisted observation", err)
	}
}

func TestStableRuleProofRetainsAllAuthorizationBoundaries(t *testing.T) {
	changes := map[string]func(*State){
		"fragment": func(s *State) {
			e := s.Deployments["node-1"]
			e.Status.RuleConfigSHA256 = strings.Repeat("c", 64)
			s.Deployments["node-1"] = e
		},
		"missing receipt identity": func(s *State) {
			e := s.Deployments["node-1"]
			e.Status.RuleConfigSHA256 = ""
			s.Deployments["node-1"] = e
		},
		"malformed proof identity": func(s *State) {
			p := s.ProtocolHealth["node-1"]
			p.RuleConfigSHA256 = "bad"
			s.ProtocolHealth["node-1"] = p
		},
		"rule revision": func(s *State) { s.Rule.Revision++ },
		"address":       func(s *State) { s.Members[0].DialHost = "changed.example.test" },
		"unverified receipt": func(s *State) {
			e := s.Deployments["node-1"]
			e.Status.ReceiptVerified = false
			s.Deployments["node-1"] = e
		},
		"expired": func(s *State) {
			p := s.ProtocolHealth["node-1"]
			p.VerifiedAt = time.Now().Add(-2 * time.Minute)
			p.LeaseExpiresAt = p.VerifiedAt.Add(endpoints.DefaultHealthTTL)
			s.ProtocolHealth["node-1"] = p
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			state := stateWithStableRuleEvidence()
			change(&state)
			if ReadyCandidateCount(state, time.Now()) != 0 {
				t.Fatal("invalid stable evidence authorized candidate")
			}
			if p := state.ProtocolHealth["node-1"]; p.MatchesCurrentDeployment(state.Pool, state.Rule, state.Members[0], state.Deployments["node-1"], time.Now()) {
				t.Fatal("invalid in-flight observation accepted")
			}
		})
	}
	legacy := stateWithVerifiedEvidence()
	e := legacy.Deployments["node-1"]
	e.Status.NodeConfigGeneration++
	e.Status.RuleConfigSHA256 = strings.Repeat("a", 64)
	legacy.Deployments["node-1"] = e
	if ReadyCandidateCount(legacy, time.Now()) != 0 {
		t.Fatal("legacy proof bypassed full-bundle validation")
	}
}

func TestStableRuleObservationOrderingIsMonotonicAcrossBundles(t *testing.T) {
	state := stateWithStableRuleEvidence()
	previous := state.ProtocolHealth["node-1"]
	newer := previous
	newer.NodeConfigGeneration++
	newer.ConfigSHA256 = strings.Repeat("b", 64)
	newer.VerifiedAt = newer.VerifiedAt.Add(time.Second)
	newer.LeaseExpiresAt = newer.LeaseExpiresAt.Add(time.Second)
	if !newer.CanReplace(previous) || !newer.CanReplace(newer) {
		t.Fatal("fresh observation or idempotent retry rejected")
	}
	if previous.CanReplace(newer) {
		t.Fatal("delayed old bundle probe rolled back newer rule lease")
	}
	late := newer
	late.VerifiedAt = previous.VerifiedAt
	if late.CanReplace(newer) {
		t.Fatal("timestamp regression accepted")
	}
}

func TestSchedulerPreservesUnchangedRuleDueTimeAcrossBundleGeneration(t *testing.T) {
	input := schedulingInput("stable-rule", 1)
	member := input.State.Members[0]
	e := input.State.Deployments[member.NodeID]
	e.Status.RuleConfigSHA256 = strings.Repeat("a", 64)
	input.State.Deployments[member.NodeID] = e
	runner := NewProtocolRunner(nil, "unused", 39000, 15*time.Second, nil)
	targets := make(map[string]*scheduledProbe)
	runner.applyRefresh([]ProbeState{input}, targets)
	key := input.State.Pool.ID + ":" + member.NodeID
	target := targets[key]
	originalJob := target.job
	due := time.Now().Add(time.Minute)
	target.due = due
	target.inFlight = 42
	e.Status.NodeConfigGeneration++
	e.Status.ConfigSHA256 = strings.Repeat("b", 64)
	input.State.Deployments = map[string]DeploymentEvidence{member.NodeID: e}
	runner.applyRefresh([]ProbeState{input}, targets)
	if !targets[key].due.Equal(due) || targets[key].inFlight != 42 || !sameProbe(originalJob, targets[key].job) {
		t.Fatal("unrelated bundle reset due time or invalidated matching in-flight challenge")
	}
	e.Status.RuleConfigSHA256 = strings.Repeat("c", 64)
	input.State.Deployments = map[string]DeploymentEvidence{member.NodeID: e}
	runner.applyRefresh([]ProbeState{input}, targets)
	if !targets[key].due.Before(due) || sameProbe(originalJob, targets[key].job) {
		t.Fatal("changed fragment was not immediately scheduled for verification")
	}
	// Even equal malformed or one-sided identities cannot weaken old matching.
	for _, pair := range [][2]string{{"bad", "bad"}, {strings.Repeat("a", 64), ""}, {"", strings.Repeat("a", 64)}} {
		if sameProbeDeployment(deploymentreceipts.Status{RuleConfigSHA256: pair[0]}, deploymentreceipts.Status{RuleConfigSHA256: pair[1]}) {
			t.Fatal("malformed probe identity matched")
		}
	}
}
