package memoryrepo

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// Install synthetic records directly so setup does not benchmark hundreds of
// preceding mutations. Every binding and node-local material still undergoes
// the ordinary authoritative compiler validation during the measured batch.
func compilerScaleFixture(t testing.TB, ruleCount, nodeCount int) (*Store, []string) {
	return compilerScaleIdentityFixture(t, ruleCount, nodeCount, false)
}

func compilerScaleIdentityFixture(t testing.TB, ruleCount, nodeCount int, unique bool) (*Store, []string) {
	t.Helper()
	f := newVLESSBundleFixture(t, true)
	s := f.store
	baseRule := s.forwardRules[f.rule.ID]
	baseBinding := s.vlessBindings[f.bindingID]
	baseMaterial := s.vlessRuntimeMaterials[f.materialID]
	basePool := s.endpointPools[f.endpointID]
	baseNode := s.nodes[f.nodeID]
	baseMember := s.membersByGroup[baseRule.EntryGroupID][f.nodeID]
	baseEndpoint := s.endpointMembers[f.endpointID][f.nodeID]
	baseUpstream := s.vlessSOCKS5Upstreams[baseRule.ID]
	delete(s.forwardRules, f.rule.ID)
	delete(s.vlessBindings, f.bindingID)
	delete(s.vlessRuntimeMaterials, f.materialID)
	delete(s.endpointPools, f.endpointID)
	delete(s.endpointMembers, f.endpointID)
	delete(s.nodes, f.nodeID)
	delete(s.nodesByCredential, baseNode.CredentialHash)
	delete(s.vlessSOCKS5Upstreams, baseRule.ID)
	delete(s.membersByGroup[baseRule.EntryGroupID], f.nodeID)
	network := s.groupNetworks[baseRule.EntryGroupID]
	network.PortEnd = 12000 + ruleCount - 1
	s.groupNetworks[baseRule.EntryGroupID] = network
	nodeIDs := make([]string, nodeCount)
	for n := range nodeIDs {
		id := fmt.Sprintf("compile-node-%02d", n)
		nodeIDs[n] = id
		node := cloneNode(baseNode)
		// The original synthetic node has no applied configuration or receipts.
		node.ID, node.Capabilities = id, []string{"xray", "vless-reality"}
		node.CredentialHash = "synthetic-credential-" + id
		s.nodes[id] = node
		s.nodesByCredential[node.CredentialHash] = id
		member := baseMember
		member.NodeID = id
		s.membersByGroup[baseRule.EntryGroupID][id] = member
	}
	for i := 0; i < ruleCount; i++ {
		id := fmt.Sprintf("compile-rule-%04d", i)
		poolID, bindingID := "pool-"+id, "binding-"+id
		rule := cloneForwardingRule(baseRule)
		rule.ID, rule.ListenPort, rule.Status = id, 12000+i, forwarding.StatusPendingActivation
		credential, privateKey := baseBinding.CredentialUUID, baseMaterial.RealityPrivateKey
		if unique {
			// Synthetic independent keys model ordinary per-customer identities.
			// Setup stays outside the measured compilation window.
			seed := sha256.Sum256([]byte("isolated-compiler-benchmark-" + id))
			privateKey = base64.RawURLEncoding.EncodeToString(seed[:])
			var err error
			rule.RealityPublicKey, err = vlessruntime.RealityPublicKey(privateKey)
			if err != nil {
				t.Fatal(err)
			}
			credential = fmt.Sprintf("00000000-0000-4000-8000-%012x", i+1)
		}
		s.forwardRules[id] = rule
		s.vlessSOCKS5Upstreams[id] = baseUpstream
		pool := cloneEndpointPool(basePool)
		pool.ID, pool.RuleID, pool.Port = poolID, id, rule.ListenPort
		pool.MemberCount = nodeCount
		s.endpointPools[poolID] = pool
		binding := cloneVLESSCredentialRecord(baseBinding)
		binding.Binding.ID, binding.Binding.ForwardingRuleID, binding.Binding.EndpointPoolID = bindingID, id, poolID
		binding.CredentialUUID = credential
		s.vlessBindings[bindingID] = binding
		s.endpointMembers[poolID] = make(map[string]endpoints.EndpointPoolMember, nodeCount)
		for _, nodeID := range nodeIDs {
			member := baseEndpoint
			member.PoolID, member.NodeID = poolID, nodeID
			s.endpointMembers[poolID][nodeID] = member
			material := cloneVLESSRuntimeMaterial(baseMaterial)
			material.ID, material.NodeID = "material-"+id+"-"+nodeID, nodeID
			material.BindingID, material.ForwardingRuleID, material.EndpointPoolID = bindingID, id, poolID
			material.CredentialUUID, material.RealityPrivateKey = credential, privateKey
			s.vlessRuntimeMaterials[material.ID] = material
		}
	}
	return s, nodeIDs
}

func BenchmarkNodeBundleDispatchCompile(b *testing.B) {
	benchmarkNodeBundleDispatchCompile(b, false)
}

func BenchmarkNodeBundleDispatchCompileUniqueIdentities(b *testing.B) {
	benchmarkNodeBundleDispatchCompile(b, true)
}

func benchmarkNodeBundleDispatchCompile(b *testing.B, unique bool) {
	for _, scale := range []struct{ rules, nodes int }{{100, 1}, {500, 3}} {
		b.Run(fmt.Sprintf("rules_%d_nodes_%d", scale.rules, scale.nodes), func(b *testing.B) {
			s, ids := compilerScaleIdentityFixture(b, scale.rules, scale.nodes, unique)
			now := time.Now().UTC()
			if err := s.recompileForwardingGroupsLocked([]string{"entry-1"}, now); err != nil {
				b.Fatal(err)
			}
			for _, id := range ids {
				if s.nodes[id].DesiredGeneration == 0 {
					b.Fatal("fixture did not compile an executable bundle")
				}
				var bundle agentv1.ConfigurationBundle
				config := s.nodeConfigsByNode[id][s.nodes[id].DesiredGeneration]
				if err := json.Unmarshal(config.Config, &bundle); err != nil || len(bundle.Fragments) != scale.rules {
					b.Fatalf("fixture fragment count=%d want=%d err=%v", len(bundle.Fragments), scale.rules, err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := s.recompileForwardingGroupsLocked([]string{"entry-1"}, now); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
