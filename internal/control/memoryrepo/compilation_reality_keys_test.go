package memoryrepo

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"maps"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestCompilationRealityKeysCompareExactPairs(t *testing.T) {
	privateKey := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{19}, 32))
	otherKey := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{29}, 32))
	publicKey, err := vlessruntime.RealityPublicKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	keys := &compilationRealityKeys{}
	for pass := 0; pass < 2; pass++ {
		for _, pair := range [][2]string{
			{privateKey, publicKey}, {otherKey, publicKey}, {privateKey, "invalid"},
			{"invalid", publicKey}, {privateKey, publicKey + "="},
			{" " + privateKey + " ", " " + publicKey + " "}, {"", ""},
		} {
			if keys.matches(pair[0], pair[1]) != vlessruntime.RealityKeyMatchesPublicKey(pair[0], pair[1]) {
				t.Fatal("memo changed cryptographic comparison result")
			}
		}
	}
}

// Warm the exact same key pair on another node before changing relationships.
// Even deliberately reusing this local memo must not bypass those checks.
func TestCompilationRealityKeysKeepNodeAndBindingChecks(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Store, string)
		want   int
	}{
		{"healthy", func(*Store, string) {}, 1},
		{"revoked_material", func(s *Store, nodeID string) {
			for id, material := range s.vlessRuntimeMaterials {
				if material.NodeID == nodeID {
					material.State = vlessruntime.StateRevoked
					s.vlessRuntimeMaterials[id] = material
				}
			}
		}, 0},
		{"wrong_customer", func(s *Store, nodeID string) {
			for id, material := range s.vlessRuntimeMaterials {
				if material.NodeID == nodeID {
					material.CustomerID = "other"
					s.vlessRuntimeMaterials[id] = material
				}
			}
		}, 0},
		{"missing_endpoint", func(s *Store, nodeID string) {
			delete(s.endpointMembers["pool-compile-rule-0000"], nodeID)
		}, 0},
		{"retired_member", func(s *Store, nodeID string) {
			member := s.membersByGroup["entry-1"][nodeID]
			at := time.Now()
			member.RetiredAt = &at
			s.membersByGroup["entry-1"][nodeID] = member
		}, 0},
		{"binding_revoked", func(s *Store, _ string) {
			binding := s.vlessBindings["binding-compile-rule-0000"]
			binding.Binding.State = vlessidentity.StateRevoked
			s.vlessBindings[binding.Binding.ID] = binding
		}, 0},
		{"public_key_changed", func(s *Store, _ string) {
			rule := s.forwardRules["compile-rule-0000"]
			rule.RealityPublicKey = "invalid"
			s.forwardRules[rule.ID] = rule
		}, 0},
		{"private_key_changed", func(s *Store, nodeID string) {
			for id, material := range s.vlessRuntimeMaterials {
				if material.NodeID == nodeID {
					material.RealityPrivateKey = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{31}, 32))
					s.vlessRuntimeMaterials[id] = material
				}
			}
		}, 0},
		{"auto_key_fallback", func(s *Store, nodeID string) {
			for id, material := range s.vlessRuntimeMaterials {
				if material.NodeID == nodeID {
					s.autoRealityKeys[material.ForwardingRuleID] = material.RealityPrivateKey
					delete(s.vlessRuntimeMaterials, id)
				}
			}
		}, 1},
		{"direct", func(s *Store, _ string) {
			rule := s.forwardRules["compile-rule-0000"]
			rule.VLESSOutboundMode = forwarding.VLESSOutboundDirect
			s.forwardRules[rule.ID] = rule
		}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, ids := compilerScaleIdentityFixture(t, 1, 3, true)
			keys := &compilationRealityKeys{}
			warm, err := s.compileForwardingFragmentsWithRealityKeysLocked(ids[0], keys)
			if err != nil || len(warm) != 1 || len(keys.results) != 1 {
				t.Fatal("failed to warm valid key comparison")
			}
			tc.change(s, ids[1])
			got, err := s.compileForwardingFragmentsWithRealityKeysLocked(ids[1], keys)
			if err != nil || len(got) != tc.want {
				t.Fatalf("fragment count=%d want=%d err=%v", len(got), tc.want, err)
			}
			want, err := s.compileForwardingFragmentsLocked(ids[1])
			if err != nil {
				t.Fatal(err)
			}
			gotRaw, _ := json.Marshal(got)
			wantRaw, _ := json.Marshal(want)
			if !bytes.Equal(gotRaw, wantRaw) {
				t.Fatal("memoized compilation changed authoritative output")
			}
		})
	}
}

func TestCompilationRealityBatchPreservesHashAndGeneration(t *testing.T) {
	s, ids := compilerScaleIdentityFixture(t, 8, 3, true)
	now := time.Now().UTC()
	for _, id := range ids {
		if _, _, err := s.compileNodeConfigLocked(id, now); err != nil {
			t.Fatal(err)
		}
	}
	before := maps.Clone(s.nodes)
	if err := s.recompileForwardingGroupsLocked([]string{"entry-1"}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if s.nodes[id].DesiredGeneration != before[id].DesiredGeneration {
			t.Fatal("memoized batch changed stable configuration hash")
		}
	}
	// A new batch must not retain a previously successful key comparison.
	rule := s.forwardRules["compile-rule-0000"]
	rule.RealityPublicKey = "invalid"
	s.forwardRules[rule.ID] = rule
	if err := s.recompileForwardingGroupsLocked([]string{"entry-1"}, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		node := s.nodes[id]
		var bundle agentv1.ConfigurationBundle
		if err := json.Unmarshal(s.nodeConfigsByNode[id][node.DesiredGeneration].Config, &bundle); err != nil {
			t.Fatal(err)
		}
		if node.DesiredGeneration != before[id].DesiredGeneration+1 || len(bundle.Fragments) != 7 {
			t.Fatal("new batch retained an invalid Reality listener")
		}
	}
}

func TestCompilationRealityBatchRollback(t *testing.T) {
	s, ids := compilerScaleIdentityFixture(t, 2, 3, true)
	now := time.Now().UTC()
	if err := s.recompileForwardingGroupsLocked([]string{"entry-1"}, now); err != nil {
		t.Fatal(err)
	}
	beforeNodes := maps.Clone(s.nodes)
	beforeConfigs := make(map[string]map[int64]generations.NodeConfigGeneration)
	for _, id := range ids {
		beforeConfigs[id] = maps.Clone(s.nodeConfigsByNode[id])
	}
	// The first node compiles a new generation before the second encounters
	// malformed group JSON. The entire multi-node batch must roll back.
	rule := s.forwardRules["compile-rule-0000"]
	rule.RealityServerName = "changed.example.test"
	s.forwardRules[rule.ID] = rule
	s.membersByGroup["bad-group"] = maps.Clone(s.membersByGroup["entry-1"])
	delete(s.membersByGroup["bad-group"], ids[0])
	s.revisionsByGroup["bad-group"] = []generations.GroupRevision{{GroupID: "bad-group", Revision: 1, Engine: agentv1.EngineXray, Config: json.RawMessage(`{`)}}
	if err := s.recompileForwardingGroupsLocked([]string{"entry-1"}, now.Add(time.Minute)); err == nil {
		t.Fatal("malformed bundle was accepted")
	}
	for _, id := range ids {
		if s.nodes[id].DesiredGeneration != beforeNodes[id].DesiredGeneration || len(s.nodeConfigsByNode[id]) != len(beforeConfigs[id]) {
			t.Fatal("batch failure did not restore prior generations")
		}
		for generation, config := range beforeConfigs[id] {
			after := s.nodeConfigsByNode[id][generation]
			if config.ConfigSHA256 != after.ConfigSHA256 || !bytes.Equal(config.Config, after.Config) {
				t.Fatal("rollback changed previously accepted configuration")
			}
		}
	}
}

func TestCompilationRealityKeysStillInspectNativeConfig(t *testing.T) {
	f, profile := nativeRuntimeFixture(t)
	service, err := vlessruntime.NewService(f.store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	material, _, err := service.BindImported(context.Background(), "admin", f.bindingID, f.nodeID, profile.Config, "memo-native-bind")
	if err != nil {
		t.Fatal(err)
	}
	rule := f.store.forwardRules[f.rule.ID]
	rule.Paused = false
	f.store.forwardRules[rule.ID] = rule
	keys := &compilationRealityKeys{}
	before, err := f.store.compileForwardingFragmentsWithRealityKeysLocked(f.nodeID, keys)
	if err != nil || len(before) != 1 || len(keys.results) != 1 {
		t.Fatal("native fixture did not warm a valid cryptographic comparison", err)
	}
	stored := f.store.vlessRuntimeMaterials[material.ID]
	stored.NativeConfig = json.RawMessage(`{`)
	f.store.vlessRuntimeMaterials[material.ID] = stored
	after, err := f.store.compileForwardingFragmentsWithRealityKeysLocked(f.nodeID, keys)
	if err != nil || len(after) != 0 {
		t.Fatal("cached key comparison bypassed native config validation", err)
	}
}
