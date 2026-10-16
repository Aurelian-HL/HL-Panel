package memoryrepo

import (
	"encoding/json"
	"testing"

	agentconfig "github.com/hongle/hl-panel/internal/agent/config"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestListenerMetadataRequiresTrafficControlCapability(t *testing.T) {
	f := newVLESSBundleFixture(t, true)
	node := f.store.nodes[f.nodeID]
	node.Capabilities = append(node.Capabilities, agentv1.CapabilityUsageGeneration)
	f.store.nodes[f.nodeID] = node
	for _, capable := range []bool{false, true} {
		if capable {
			node.Capabilities = append(node.Capabilities, agentconfig.CapabilityLinuxTCSpeedLimit)
			f.store.nodes[f.nodeID] = node
		}
		compiled, _, err := f.store.compileNodeConfigLocked(f.nodeID, f.rule.CreatedAt)
		if err != nil {
			t.Fatal(err)
		}
		var bundle agentv1.ConfigurationBundle
		if err := json.Unmarshal(compiled.Config, &bundle); err != nil {
			t.Fatal(err)
		}
		if len(bundle.Fragments) != 1 || bundle.Fragments[0].Usage == nil {
			t.Fatal("missing executable fragment or accounting metadata")
		}
		want := 0
		if capable {
			want = f.rule.ListenPort
		}
		if bundle.Fragments[0].Usage.ListenPort != want {
			t.Fatal("listener metadata did not follow agent capability")
		}
	}
}

func TestListenerMetadataPreservesLegacyAcceptedShape(t *testing.T) {
	f := newVLESSBundleFixture(t, true)
	node := f.store.nodes[f.nodeID]
	node.Capabilities = append(node.Capabilities, agentv1.CapabilityUsageGeneration, agentconfig.CapabilityLinuxTCSpeedLimit)
	f.store.nodes[f.nodeID] = node
	first, _, err := f.store.compileNodeConfigLocked(f.nodeID, f.rule.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	node = f.store.nodes[f.nodeID]
	filtered := node.Capabilities[:0:0]
	for _, capability := range node.Capabilities {
		if capability != agentconfig.CapabilityLinuxTCSpeedLimit {
			filtered = append(filtered, capability)
		}
	}
	node.Capabilities = filtered
	f.store.nodes[f.nodeID] = node
	next, changed, err := f.store.compileNodeConfigLocked(f.nodeID, f.rule.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if changed || first.ConfigSHA256 != next.ConfigSHA256 {
		t.Fatal("legacy accepted listener metadata caused a configuration reload")
	}
}
