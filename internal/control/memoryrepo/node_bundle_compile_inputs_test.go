package memoryrepo

import (
	"bytes"
	"encoding/base64"
	"reflect"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
)

// Compare the optimized full compilation with the previous public-view input
// and full-map validation. Mutate only after an initial successful compile so
// these cases also detect an index accidentally retained across mutations.
func TestNodeCompilationInputsPreserveAuthoritativeChecks(t *testing.T) {
	rotatedKey := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{23}, 32))
	rotatedPublic, err := vlessruntime.RealityPublicKey(rotatedKey)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		change func(vlessBundleFixture)
		ready  bool
	}{
		{"healthy", func(vlessBundleFixture) {}, true},
		{"paused", func(f vlessBundleFixture) {
			r := f.store.forwardRules[f.rule.ID]
			r.Paused = true
			f.store.forwardRules[r.ID] = r
		}, false},
		{"rule_quota", func(f vlessBundleFixture) {
			r := f.store.forwardRules[f.rule.ID]
			r.TrafficLimitBytes = 10
			r.TrafficUsedBytes = 10
			f.store.forwardRules[r.ID] = r
		}, false},
		{"missing_owner", func(f vlessBundleFixture) { clear(f.store.adminsByUsername) }, false},
		{"binding_revoked", func(f vlessBundleFixture) {
			b := f.store.vlessBindings[f.bindingID]
			b.Binding.State = vlessidentity.StateRevoked
			f.store.vlessBindings[f.bindingID] = b
		}, false},
		{"binding_wrong_customer", func(f vlessBundleFixture) {
			b := f.store.vlessBindings[f.bindingID]
			b.Binding.CustomerID = "other"
			f.store.vlessBindings[f.bindingID] = b
		}, false},
		{"material_revoked_blocks_auto_key", func(f vlessBundleFixture) {
			m := f.store.vlessRuntimeMaterials[f.materialID]
			m.State = vlessruntime.StateRevoked
			f.store.vlessRuntimeMaterials[f.materialID] = m
			f.store.autoRealityKeys[f.rule.ID] = f.privateKey
		}, false},
		{"invalid_private_key", func(f vlessBundleFixture) {
			m := f.store.vlessRuntimeMaterials[f.materialID]
			m.RealityPrivateKey = "invalid"
			f.store.vlessRuntimeMaterials[f.materialID] = m
		}, false},
		{"wrong_public_key", func(f vlessBundleFixture) {
			r := f.store.forwardRules[f.rule.ID]
			r.RealityPublicKey = "invalid"
			f.store.forwardRules[r.ID] = r
		}, false},
		{"material_wrong_rule", func(f vlessBundleFixture) {
			m := f.store.vlessRuntimeMaterials[f.materialID]
			m.ForwardingRuleID = "other"
			f.store.vlessRuntimeMaterials[f.materialID] = m
		}, false},
		{"retired_member", func(f vlessBundleFixture) {
			m := f.store.membersByGroup[f.rule.EntryGroupID][f.nodeID]
			at := time.Now()
			m.RetiredAt = &at
			f.store.membersByGroup[f.rule.EntryGroupID][f.nodeID] = m
		}, false},
		{"missing_endpoint_member", func(f vlessBundleFixture) { delete(f.store.endpointMembers[f.endpointID], f.nodeID) }, false},
		{"missing_capability", func(f vlessBundleFixture) {
			n := f.store.nodes[f.nodeID]
			n.Capabilities = nil
			f.store.nodes[f.nodeID] = n
		}, false},
		{"missing_upstream", func(f vlessBundleFixture) { delete(f.store.vlessSOCKS5Upstreams, f.rule.ID) }, false},
		{"unsupported_connection_limit", func(f vlessBundleFixture) {
			r := f.store.forwardRules[f.rule.ID]
			r.ConnectionLimit = 3
			f.store.forwardRules[r.ID] = r
		}, false},
		{"unsupported_speed_limit", func(f vlessBundleFixture) {
			r := f.store.forwardRules[f.rule.ID]
			r.SpeedLimitMbps = 10
			f.store.forwardRules[r.ID] = r
		}, false},
		{"auto_key_fallback", func(f vlessBundleFixture) {
			delete(f.store.vlessRuntimeMaterials, f.materialID)
			f.store.autoRealityKeys[f.rule.ID] = f.privateKey
		}, true},
		{"unrelated_corrupt_material", func(f vlessBundleFixture) {
			m := f.store.vlessRuntimeMaterials[f.materialID]
			m.ID, m.NodeID, m.RealityPrivateKey = "unrelated", "other-node", "invalid"
			f.store.vlessRuntimeMaterials[m.ID] = m
		}, true},
		{"changed_sni", func(f vlessBundleFixture) {
			r := f.store.forwardRules[f.rule.ID]
			r.RealityServerName, r.RealityDestination = "new.example.test", "new.example.test:443"
			f.store.forwardRules[r.ID] = r
		}, true},
		{"key_rotation", func(f vlessBundleFixture) {
			r := f.store.forwardRules[f.rule.ID]
			r.RealityPublicKey = rotatedPublic
			f.store.forwardRules[r.ID] = r
			m := f.store.vlessRuntimeMaterials[f.materialID]
			m.RealityPrivateKey = rotatedKey
			f.store.vlessRuntimeMaterials[f.materialID] = m
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newVLESSBundleFixture(t, true)
			before, err := f.store.compileForwardingFragmentsLocked(f.nodeID)
			if err != nil || len(before) != 1 {
				t.Fatal("invalid fixture", err)
			}
			tc.change(f)
			stored := f.store.forwardRules[f.rule.ID]
			legacy := f.store.forwardingBaseViewLocked(stored)
			legacy.VLESSSOCKS5Username = stored.VLESSSOCKS5Username
			want, ready, err := f.store.compileVLESSRealityFragmentLocked(f.nodeID, legacy)
			if err != nil || ready != tc.ready {
				t.Fatalf("reference ready=%v want=%v err=%v", ready, tc.ready, err)
			}
			actual, err := f.store.compileForwardingFragmentsLocked(f.nodeID)
			if err != nil || (len(actual) == 1) != tc.ready || len(actual) > 1 {
				t.Fatalf("optimized count=%d ready=%v err=%v", len(actual), tc.ready, err)
			}
			if ready && !reflect.DeepEqual(actual[0], want) {
				t.Fatal("compiled fragment changed")
			}
			if ready && (tc.name == "key_rotation" || tc.name == "changed_sni") && bytes.Equal(before[0].Config, actual[0].Config) {
				t.Fatal("mutation did not reach next compile")
			}
		})
	}
}

func TestNodeCompilationInputsEnforceCustomerLifecycle(t *testing.T) {
	for _, kind := range []string{"disabled", "expired", "quota"} {
		t.Run(kind, func(t *testing.T) {
			f := newOwnedVLESSBundleFixture(t, true, "cus-1")
			if fs, err := f.store.compileForwardingFragmentsLocked(f.nodeID); err != nil || len(fs) != 1 {
				t.Fatal("invalid customer fixture", err)
			}
			c := f.store.customers[f.rule.CustomerID]
			switch kind {
			case "disabled":
				c.Disabled = true
			case "expired":
				at := time.Now().Add(-time.Minute)
				c.ExpiresAt = &at
			case "quota":
				c.TrafficLimitBytes, c.TrafficUsedBytes = 10, 10
			}
			f.store.customers[f.rule.CustomerID] = c
			if fs, err := f.store.compileForwardingFragmentsLocked(f.nodeID); err != nil || len(fs) != 0 {
				t.Fatal("customer restriction bypassed", err)
			}
		})
	}
}
