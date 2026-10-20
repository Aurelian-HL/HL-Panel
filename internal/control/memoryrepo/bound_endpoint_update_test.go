package memoryrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func boundPortRequest(r forwarding.Rule) forwarding.Request {
	request := requestFromNative(r)
	request.ExitGroupID = r.ExitGroupID
	request.TunnelTransport = r.TunnelTransport
	request.VLESSSOCKS5Host = r.VLESSSOCKS5Host
	request.VLESSSOCKS5Port = r.VLESSSOCKS5Port
	// Omitting the write-only credentials must preserve the existing upstream.
	request.SpeedLimitMbps, request.IPLimit, request.ConnectionLimit = r.SpeedLimitMbps, r.IPLimit, r.ConnectionLimit
	request.TrafficLimitBytes, request.TrafficQuotaMonthly = r.TrafficLimitBytes, r.TrafficQuotaMonthly
	return request
}

func boundPortSnapshot(t *testing.T, s *Store) []byte {
	t.Helper()
	raw, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestBoundEndpointPortUpdatePreservesIdentityAndInvalidatesHealth(t *testing.T) {
	f, proof := compiledRuleReadinessFixture(t)
	s, ctx := f.store, context.Background()
	rule := s.forwardRules[f.rule.ID]
	rule.TrafficUsedBytes = 12345
	s.forwardRules[rule.ID] = rule
	s.autoRealityKeys[rule.ID] = f.privateKey
	poolBefore := s.endpointPools[f.endpointID]
	member := s.endpointMembers[f.endpointID][f.nodeID]
	member.ActiveConnections, member.Weight, member.Priority = 7, 3, 2
	s.endpointMembers[f.endpointID][f.nodeID] = member
	identityBefore := s.vlessBindings[f.bindingID]
	materialBefore := s.vlessRuntimeMaterials[f.materialID]
	upstreamBefore := s.vlessSOCKS5Upstreams[rule.ID]
	probeBefore := s.protocolProbes[rule.ID]
	configBefore := s.nodeConfigsByNode[f.nodeID][s.nodes[f.nodeID].DesiredGeneration]
	if _, _, err := s.DesiredNodeConfig(ctx, f.nodeID); !errors.Is(err, faults.ErrNotFound) {
		t.Fatal("applied fixture unexpectedly has pending configuration", err)
	}
	request := boundPortRequest(rule)
	request.ListenPort++
	service := forwarding.NewService(s, time.Now)
	updated, replayed, err := service.UpdateForAdministrator(ctx, "admin", rule.ID, request, "bound-port-update")
	if err != nil || replayed || updated.ID != rule.ID || updated.Revision != rule.Revision+1 || updated.ListenPort != request.ListenPort || updated.Deployed {
		t.Fatal("bound port update failed or advertised unapplied configuration", err)
	}
	pool := s.endpointPools[f.endpointID]
	if pool.ID != poolBefore.ID || pool.RuleID != rule.ID || pool.GroupID != poolBefore.GroupID || pool.Hostname != poolBefore.Hostname || pool.Port != updated.ListenPort || !pool.CreatedAt.Equal(poolBefore.CreatedAt) {
		t.Fatal("bound endpoint identity or address was not preserved")
	}
	if !reflect.DeepEqual(s.vlessBindings[f.bindingID], identityBefore) || !reflect.DeepEqual(s.vlessRuntimeMaterials[f.materialID], materialBefore) || !reflect.DeepEqual(s.vlessSOCKS5Upstreams[rule.ID], upstreamBefore) || s.autoRealityKeys[rule.ID] != f.privateKey || s.protocolProbes[rule.ID] != probeBefore {
		t.Fatal("port update rotated or removed identity, upstream or key material")
	}
	if updated.TrafficUsedBytes != rule.TrafficUsedBytes || updated.RealityPublicKey != rule.RealityPublicKey || updated.RealityShortID != rule.RealityShortID || updated.RealityServerName != rule.RealityServerName || updated.VLESSFlow != rule.VLESSFlow {
		t.Fatal("port update changed traffic or Reality parameters")
	}
	currentMember := s.endpointMembers[f.endpointID][f.nodeID]
	if currentMember.LastHealthAt != nil || currentMember.LastHealthReason != "listener_port_changed" || currentMember.ActiveConnections != 7 || currentMember.Weight != 3 || currentMember.Priority != 2 || currentMember.State != member.State || len(s.protocolHealth[f.endpointID]) != 0 {
		t.Fatal("port update kept stale health or changed connection/member state")
	}
	if err := s.PublishProtocolHealth(ctx, proof); !errors.Is(err, faults.ErrValidation) {
		t.Fatal("old-port proof was accepted", err)
	}
	assertCompiledRuleReady(t, f, false)
	configAfter, _, err := s.DesiredNodeConfig(ctx, f.nodeID)
	if err != nil || configAfter.Generation != configBefore.Generation+1 || configAfter.ConfigSHA256 == configBefore.ConfigSHA256 {
		t.Fatal("port update did not generate new node configuration", err)
	}
	fragments, err := s.compileForwardingFragmentsLocked(f.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, fragment := range fragments {
		if fragment.GroupID != generations.ForwardingFragmentID("xray", rule.ID) {
			continue
		}
		var config struct {
			Inbounds []struct {
				Port int `json:"port"`
			} `json:"inbounds"`
		}
		if err := json.Unmarshal(fragment.Config, &config); err != nil || len(config.Inbounds) == 0 || config.Inbounds[0].Port != updated.ListenPort {
			t.Fatal("new port absent from compiled Xray listener", err)
		}
		found = true
	}
	if !found {
		t.Fatal("updated listener disappeared from node bundle")
	}
	beforeReplay := boundPortSnapshot(t, s)
	if replay, ok, err := service.UpdateForAdministrator(ctx, "admin", rule.ID, request, "bound-port-update"); err != nil || !ok || replay.Revision != updated.Revision || replay.ID != rule.ID {
		t.Fatal("same mutation did not replay", err)
	}
	if !bytes.Equal(beforeReplay, boundPortSnapshot(t, s)) {
		t.Fatal("idempotent replay changed stored state")
	}
	if _, _, err := service.UpdateForAdministrator(ctx, "admin", rule.ID, request, "stale-port-update"); !errors.Is(err, faults.ErrConflict) {
		t.Fatal("stale revision was accepted", err)
	}
	request.ListenPort++
	if _, _, err := service.UpdateForAdministrator(ctx, "admin", rule.ID, request, "bound-port-update"); !errors.Is(err, faults.ErrIdempotencyConflict) {
		t.Fatal("same key with different port was accepted", err)
	}
	restored, err := DecodeSnapshot(boundPortSnapshot(t, s))
	if err != nil {
		t.Fatal("updated endpoint snapshot could not be restored", err)
	}
	if restored.endpointPools[f.endpointID].Port != updated.ListenPort || !reflect.DeepEqual(restored.vlessBindings[f.bindingID], identityBefore) || restored.autoRealityKeys[rule.ID] != f.privateKey || restored.forwardRules[rule.ID].TrafficUsedBytes != 12345 || len(restored.protocolHealth[f.endpointID]) != 0 {
		t.Fatal("snapshot round trip lost endpoint, identity, usage or health invalidation")
	}
}

func TestBoundEndpointPortUpdateRejectsUnsafeChanges(t *testing.T) {
	for _, name := range []string{"range", "shared_machine", "reserved_endpoint", "different_group", "different_protocol", "multiple_pools", "foreign_owner"} {
		t.Run(name, func(t *testing.T) {
			f, _ := compiledRuleReadinessFixture(t)
			s := f.store
			request := boundPortRequest(s.forwardRules[f.rule.ID])
			request.ListenPort++
			admin := "admin"
			want := faults.ErrConflict
			switch name {
			case "range":
				request.ListenPort = 13000
				want = faults.ErrValidation
			case "shared_machine":
				s.membersByGroup["other-entry"] = map[string]groups.Member{f.nodeID: {GroupID: "other-entry", NodeID: f.nodeID, Weight: 1}}
				s.forwardRules["paused-shared-tcp"] = forwarding.Rule{ID: "paused-shared-tcp", EntryGroupID: "other-entry", Protocol: forwarding.ProtocolTCP, IngressProtocol: forwarding.IngressTCP, ListenPort: request.ListenPort, Paused: true}
			case "reserved_endpoint":
				other := s.endpointPools[f.endpointID]
				other.ID, other.RuleID, other.Port = "unbound-pool", "", request.ListenPort
				s.endpointPools[other.ID] = other
			case "different_group":
				other := s.deviceGroups[request.EntryGroupID]
				other.ID = "other-entry"
				s.deviceGroups[other.ID] = other
				network := s.groupNetworks[request.EntryGroupID]
				network.GroupID = other.ID
				s.groupNetworks[other.ID] = network
				request.EntryGroupID = other.ID
			case "different_protocol":
				request.IngressProtocol = forwarding.IngressTCP
				request.VLESSOutboundMode = ""
				request.VLESSSOCKS5Host, request.VLESSSOCKS5Username = "", ""
				request.VLESSSOCKS5Port = 0
				request.VLESSFlow, request.RealityServerName, request.RealityDestination, request.RealityPublicKey, request.RealityShortID = "", "", "", "", ""
			case "multiple_pools":
				other := s.endpointPools[f.endpointID]
				other.ID = "duplicate-bound-pool"
				s.endpointPools[other.ID] = other
			case "foreign_owner":
				admin = "another-admin"
				want = faults.ErrNotFound
			}
			before := boundPortSnapshot(t, s)
			if _, _, err := forwarding.NewService(s, time.Now).UpdateForAdministrator(context.Background(), admin, f.rule.ID, request, "unsafe-port-change"); !errors.Is(err, want) {
				t.Fatal("unsafe change was not rejected with the expected error", err)
			}
			if !bytes.Equal(before, boundPortSnapshot(t, s)) {
				t.Fatal("rejected port update changed persistent state")
			}
		})
	}
}

func TestBoundEndpointPortUpdateRollsBackCompileFailure(t *testing.T) {
	f, _ := compiledRuleReadinessFixture(t)
	s := f.store
	before := boundPortSnapshot(t, s)
	// Inject invalid fragment bytes only after capturing a serializable snapshot.
	// The failed compilation must preserve the injected revision as well as all
	// business state; removing the injection then permits a complete comparison.
	invalid := []generations.GroupRevision{{GroupID: f.rule.EntryGroupID, Revision: 1, Engine: agentv1.EngineXray, Config: json.RawMessage(`{broken`)}}
	s.revisionsByGroup[f.rule.EntryGroupID] = invalid
	request := boundPortRequest(s.forwardRules[f.rule.ID])
	request.ListenPort++
	service := forwarding.NewService(s, time.Now)
	if _, _, err := service.UpdateForAdministrator(context.Background(), "admin", f.rule.ID, request, "retry-port-change"); err == nil {
		t.Fatal("invalid bundle compiled")
	}
	if !reflect.DeepEqual(s.revisionsByGroup[f.rule.EntryGroupID], invalid) {
		t.Fatal("failed compilation changed the injected revision")
	}
	delete(s.revisionsByGroup, f.rule.EntryGroupID)
	if !bytes.Equal(before, boundPortSnapshot(t, s)) {
		t.Fatal("compile failure changed rule, endpoint, health, identity, audit or idempotency")
	}
	if _, replay, err := service.UpdateForAdministrator(context.Background(), "admin", f.rule.ID, request, "retry-port-change"); err != nil || replay {
		t.Fatal("failed mutation could not be retried with the same key", err)
	}
}

func TestBoundEndpointPortUpdateKeepsNativeRuntimeImmutable(t *testing.T) {
	f, _ := nativeRuntimeFixture(t)
	request := requestFromNative(f.rule)
	request.ListenPort++
	before := boundPortSnapshot(t, f.store)
	if _, _, err := forwarding.NewService(f.store, time.Now).Update(context.Background(), "admin", f.rule.ID, request, "native-port-change"); !errors.Is(err, faults.ErrValidation) {
		t.Fatal("imported runtime port became editable", err)
	}
	if !bytes.Equal(before, boundPortSnapshot(t, f.store)) {
		t.Fatal("rejected native port update changed persistent state")
	}
}

func TestBoundEndpointPortUpdateSerializesConcurrentEdits(t *testing.T) {
	f, _ := compiledRuleReadinessFixture(t)
	s := f.store
	var wg sync.WaitGroup
	results := make(chan error, 2)
	baseRequest := boundPortRequest(s.forwardRules[f.rule.ID])
	for i := 1; i <= 2; i++ {
		request := baseRequest
		request.ListenPort += i
		wg.Add(1)
		go func(request forwarding.Request) {
			defer wg.Done()
			_, _, err := forwarding.NewService(s, time.Now).UpdateForAdministrator(context.Background(), "admin", f.rule.ID, request, "concurrent-"+strconv.Itoa(request.ListenPort))
			results <- err
		}(request)
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, faults.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 || s.forwardRules[f.rule.ID].Revision != f.rule.Revision+1 || s.endpointPools[f.endpointID].Port != s.forwardRules[f.rule.ID].ListenPort {
		t.Fatal("concurrent edits did not keep one rule/endpoint revision")
	}
}
