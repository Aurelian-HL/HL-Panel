package memoryrepo

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// These are compiled-runtime fixtures and repository authorization tests. They
// do not claim to run a real Xray process or a network protocol challenge.
func compiledRuleReadinessFixture(t *testing.T) (vlessBundleFixture, gatewaymembership.ProtocolObservation) {
	t.Helper()
	f := newVLESSBundleFixture(t, true)
	s := f.store
	pool := s.endpointPools[f.endpointID]
	pool.Port = f.rule.ListenPort
	s.endpointPools[f.endpointID] = pool
	member := s.endpointMembers[f.endpointID][f.nodeID]
	member.DialHost = "node.example.test"
	s.endpointMembers[f.endpointID][f.nodeID] = member
	if err := s.ConfigureProtocolProbe(context.Background(), f.rule.ID, gatewaymembership.ProtocolProbeConfig{UUID: "b38caa5e-3449-4ded-b66a-7428f13ad107", EchoPort: 39000}); err != nil {
		t.Fatal(err)
	}
	attestCompiledFixture(t, f)
	input, err := s.RuleNodeDeploymentInput(context.Background(), f.rule.ID, f.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	receipt := deploymentreceipts.Evaluate(input)
	if !receipt.ReceiptVerified || len(receipt.RuleConfigSHA256) != 64 {
		t.Fatal("compiled fixture receipt not verified")
	}
	now := time.Now().UTC()
	proof := gatewaymembership.ProtocolObservation{RuleID: f.rule.ID, NodeID: f.nodeID, RuleRevision: f.rule.Revision, NodeConfigGeneration: receipt.NodeConfigGeneration, ConfigSHA256: receipt.ConfigSHA256, RuleConfigSHA256: receipt.RuleConfigSHA256, Protocol: forwarding.IngressVLESSReality, DialHost: member.DialHost, VerifiedAt: now, LeaseExpiresAt: now.Add(endpoints.DefaultHealthTTL)}
	rejected, err := s.PublishProtocolHealthBatch(context.Background(), []gatewaymembership.ProtocolObservation{proof})
	if err != nil || len(rejected) != 1 || rejected[0] != nil {
		t.Fatal("compiled fixture failed protocol activation", err, rejected)
	}
	assertCompiledRuleReady(t, f, true)
	return f, proof
}

func attestCompiledFixture(t *testing.T, f vlessBundleFixture) {
	t.Helper()
	s := f.store
	node := s.nodes[f.nodeID]
	config, exists := s.nodeConfigsByNode[f.nodeID][node.DesiredGeneration]
	if !exists {
		t.Fatal("compiled fixture config absent")
	}
	var bundle agentv1.ConfigurationBundle
	if err := json.Unmarshal(config.Config, &bundle); err != nil {
		t.Fatal(err)
	}
	xray, gost := false, false
	for _, fragment := range bundle.Fragments {
		xray = xray || fragment.Engine == agentv1.EngineXray
		gost = gost || fragment.Engine == agentv1.EngineGOST
	}
	mode := "xray"
	if xray && gost {
		mode = "mixed"
	} else if gost {
		mode = "gost"
	}
	node.AppliedGeneration = node.DesiredGeneration
	s.nodes[f.nodeID] = node
	const attemptID = "0123456789abcdef0123456789abcdef"
	if s.applyAttemptsByNode[f.nodeID] == nil {
		s.applyAttemptsByNode[f.nodeID] = make(map[int64]generations.ApplyAttemptState)
	}
	if s.applyResultsByNode[f.nodeID] == nil {
		s.applyResultsByNode[f.nodeID] = make(map[int64]map[string]generations.ApplyResult)
	}
	s.applyAttemptsByNode[f.nodeID][node.DesiredGeneration] = generations.ApplyAttemptState{CurrentID: attemptID, SeenIDs: map[string]bool{attemptID: true}}
	results := make(map[string]generations.ApplyResult)
	for _, phase := range []agentv1.ApplyPhase{agentv1.ApplyPhaseCommit, agentv1.ApplyPhaseVerify} {
		results[string(phase)] = generations.ApplyResult{NodeID: f.nodeID, Generation: node.DesiredGeneration, ConfigSHA256: config.ConfigSHA256, Phase: phase, Status: agentv1.ApplyStatusSucceeded, EngineMode: mode, AttemptID: attemptID}
	}
	s.applyResultsByNode[f.nodeID][node.DesiredGeneration] = results
}

func assertCompiledRuleReady(t *testing.T, f vlessBundleFixture, ready bool) {
	t.Helper()
	for i := 0; i < 2; i++ {
		rule, err := f.store.ForwardingRule(context.Background(), f.rule.ID)
		state, stateErr := f.store.GatewayMembershipState(context.Background(), f.endpointID)
		if err != nil || stateErr != nil || rule.Deployed != ready || (gatewaymembership.ReadyCandidateCount(state, time.Now()) > 0) != ready {
			t.Fatalf("compiled rule readiness disagrees: ready=%v deployed=%v reason=%s errors=%v/%v", ready, rule.Deployed, rule.ActivationReason, err, stateErr)
		}
	}
}

func createAdditionalCompiledRule(t *testing.T, f vlessBundleFixture) forwarding.Rule {
	t.Helper()
	request := forwarding.Request{Name: "additional unrelated same-group rule", CustomerID: f.rule.CustomerID, EntryGroupID: f.rule.EntryGroupID, EgressMode: forwarding.EgressDirect, Protocol: forwarding.ProtocolTCP, ListenPort: f.rule.ListenPort + 1, Targets: []forwarding.Target{{Host: "landing.example.test", Port: 1080}}, SelectionPolicy: forwarding.SelectionRoundRobin}
	rule, _, err := forwarding.NewService(f.store, nil).CreateForAdministrator(context.Background(), "admin", request, "stable-fragment-additional-rule")
	if err != nil {
		t.Fatal(err)
	}
	return rule
}

func TestCompiledUnchangedRuleStaysReadyDuringSameGroupAdditionAndApply(t *testing.T) {
	f, proof := compiledRuleReadinessFixture(t)
	s := f.store
	added := createAdditionalCompiledRule(t, f)
	if s.nodes[f.nodeID].DesiredGeneration <= proof.NodeConfigGeneration {
		t.Fatal("additional rule did not create a new bundle")
	}
	assertCompiledRuleReady(t, f, true)
	input, err := s.RuleNodeDeploymentInput(context.Background(), added.ID, f.nodeID)
	if err != nil || deploymentreceipts.Evaluate(input).ReceiptVerified {
		t.Fatal("new rule was falsely marked deployed before applying its bundle", err)
	}
	// An in-flight result from the unchanged listener is still valid while the
	// new bundle is pending. Publishing it never changes its original timestamps.
	if err = s.PublishProtocolHealth(context.Background(), proof); err != nil {
		t.Fatal("unchanged in-flight proof rejected", err)
	}
	attestCompiledFixture(t, f)
	assertCompiledRuleReady(t, f, true)
	if err = s.PublishProtocolHealth(context.Background(), proof); err != nil {
		t.Fatal("unchanged prior bundle proof rejected after current verify", err)
	}
	current := s.protocolHealth[f.endpointID][f.nodeID]
	if !current.VerifiedAt.Equal(proof.VerifiedAt) || !current.LeaseExpiresAt.Equal(proof.LeaseExpiresAt) || current.NodeConfigGeneration != proof.NodeConfigGeneration {
		t.Fatal("unrelated bundle rewrote or extended existing protocol observation")
	}
	state, err := s.GatewayMembershipState(context.Background(), f.endpointID)
	if err != nil || gatewaymembership.ReadyCandidateCount(state, proof.LeaseExpiresAt) != 0 {
		t.Fatal("old lease was silently renewed during bundle replacement", err)
	}
	// Public repository copies must not permit mutation of live config buffers.
	input, err = s.RuleNodeDeploymentInput(context.Background(), f.rule.ID, f.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	input.CurrentConfig.Config[0] = '!'
	assertCompiledRuleReady(t, f, true)
}

func TestAppliedConfigCloneAndHistoryCompactionPreservePendingRuleReadiness(t *testing.T) {
	f, _ := compiledRuleReadinessFixture(t)
	createAdditionalCompiledRule(t, f)
	s := f.store
	s.DetachHistoricalNodeConfigs()
	input, err := s.RuleNodeDeploymentInput(context.Background(), f.rule.ID, f.nodeID)
	if err != nil || input.AppliedConfig == nil {
		t.Fatal("hot applied config lost during compaction", err)
	}
	input.AppliedConfig.Config[0] = '!'
	input.AppliedResults[agentv1.ApplyPhaseVerify] = generations.ApplyResult{}
	assertCompiledRuleReady(t, f, true)
}

func TestCompiledRuleFragmentAndKeyChangesRequireFreshProtocolProof(t *testing.T) {
	cases := map[string]func(*testing.T, vlessBundleFixture){
		"SOCKS5 secret": func(t *testing.T, f vlessBundleFixture) {
			upstream, ok := f.store.vlessSOCKS5UpstreamLocked(f.rule.ID)
			if !ok {
				t.Fatal("fixture SOCKS5 material absent")
			}
			upstream.Password = "rotated-local-fixture-password"
			f.store.vlessSOCKS5Upstreams[f.rule.ID] = upstream
		},
		"client identity": func(t *testing.T, f vlessBundleFixture) {
			const credential = "4f984545-f3d7-4cf6-8e2c-4d615c047d83"
			binding := f.store.vlessBindings[f.bindingID]
			binding.CredentialUUID = credential
			f.store.vlessBindings[f.bindingID] = binding
			material := f.store.vlessRuntimeMaterials[f.materialID]
			material.CredentialUUID = credential
			f.store.vlessRuntimeMaterials[f.materialID] = material
		},
		"Reality key": func(t *testing.T, f vlessBundleFixture) {
			raw := make([]byte, 32)
			if _, err := rand.Read(raw); err != nil {
				t.Fatal(err)
			}
			privateKey := base64.RawURLEncoding.EncodeToString(raw)
			publicKey, err := vlessruntime.RealityPublicKey(privateKey)
			if err != nil {
				t.Fatal(err)
			}
			material := f.store.vlessRuntimeMaterials[f.materialID]
			material.RealityPrivateKey = privateKey
			f.store.vlessRuntimeMaterials[f.materialID] = material
			rule := f.store.forwardRules[f.rule.ID]
			rule.RealityPublicKey = publicKey
			f.store.forwardRules[f.rule.ID] = rule
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f, proof := compiledRuleReadinessFixture(t)
			change(t, f)
			if err := f.store.recompileForwardingGroupsLocked([]string{f.rule.EntryGroupID}, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			assertCompiledRuleReady(t, f, false)
			if err := f.store.PublishProtocolHealth(context.Background(), proof); err == nil {
				t.Fatal("old in-flight proof authorized changed pending fragment")
			}
			attestCompiledFixture(t, f)
			assertCompiledRuleReady(t, f, false)
			if err := f.store.PublishProtocolHealth(context.Background(), proof); err == nil {
				t.Fatal("old in-flight proof authorized changed verified fragment")
			}
		})
	}
}

func TestConcurrentUnrelatedRuleAdditionRetainsProofAndReadiness(t *testing.T) {
	f, proof := compiledRuleReadinessFixture(t)
	var wg sync.WaitGroup
	failures := make(chan error, 3)
	started := make(chan struct{})
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-started
			for j := 0; j < 30; j++ {
				state, err := f.store.GatewayMembershipState(context.Background(), f.endpointID)
				if err != nil || gatewaymembership.ReadyCandidateCount(state, time.Now()) != 1 {
					failures <- fmt.Errorf("concurrent readiness changed: %v", err)
					return
				}
				if err = f.store.PublishProtocolHealth(context.Background(), proof); err != nil {
					failures <- fmt.Errorf("unchanged proof rejected during add: %w", err)
					return
				}
			}
		}()
	}
	close(started)
	createAdditionalCompiledRule(t, f)
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	assertCompiledRuleReady(t, f, true)
}

// Capture before the mutation, then release publication only after the current
// runtime fragment changes. This exercises the write-time race boundary rather
// than permitting a proof that was current when its network probe began.
func TestConcurrentChangedRuleRejectsInFlightProtocolPublication(t *testing.T) {
	f, proof := compiledRuleReadinessFixture(t)
	captured := make(chan struct{})
	publish := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(captured)
		<-publish
		result <- f.store.PublishProtocolHealth(context.Background(), proof)
	}()
	<-captured
	f.store.mu.Lock()
	upstream, ok := f.store.vlessSOCKS5UpstreamLocked(f.rule.ID)
	if !ok {
		f.store.mu.Unlock()
		t.Fatal("fixture upstream missing")
	}
	upstream.Password = "changed-while-probe-in-flight"
	f.store.vlessSOCKS5Upstreams[f.rule.ID] = upstream
	compileErr := f.store.recompileForwardingGroupsLocked([]string{f.rule.EntryGroupID}, time.Now().UTC())
	f.store.mu.Unlock()
	close(publish)
	err := <-result
	if compileErr != nil {
		t.Fatal(compileErr)
	}
	if err == nil {
		t.Fatal("old in-flight protocol proof authorized changed configuration")
	}
	assertCompiledRuleReady(t, f, false)
}
