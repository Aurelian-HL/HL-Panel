package memoryrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	agentconfig "github.com/hongle/hl-panel/internal/agent/config"
	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/nodes"
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
	node.AppliedGeneration = first.Generation
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

func TestRejectedListenerMetadataReconcilesOnceWithoutChangingEngineConfig(t *testing.T) {
	for _, applied := range []bool{false, true} {
		t.Run(map[bool]string{false: "never_applied", true: "older_applied_bundle"}[applied], func(t *testing.T) {
			f := newVLESSBundleFixture(t, true)
			node := f.store.nodes[f.nodeID]
			node.Capabilities = append(node.Capabilities, agentv1.CapabilityUsageGeneration)
			f.store.nodes[f.nodeID] = node
			accepted, _, err := f.store.compileNodeConfigLocked(f.nodeID, f.rule.CreatedAt)
			if err != nil {
				t.Fatal(err)
			}
			node = f.store.nodes[f.nodeID]
			if applied {
				node.AppliedGeneration = accepted.Generation
			}
			legacyCapabilities := append([]string(nil), node.Capabilities...)
			node.Capabilities = append(node.Capabilities, agentconfig.CapabilityLinuxTCSpeedLimit)
			f.store.nodes[f.nodeID] = node
			rejected, _, err := f.store.compileNodeConfigLocked(f.nodeID, f.rule.CreatedAt)
			if err != nil {
				t.Fatal(err)
			}
			node = f.store.nodes[f.nodeID]
			node.Capabilities = legacyCapabilities
			node.BundleCompilerRevision = NodeBundleCompilerRevision - 1
			node.LastApplyGeneration = rejected.Generation
			node.LastApplyStatus = "rolled_back"
			f.store.nodes[f.nodeID] = node
			heartbeat := nodes.Heartbeat{Capabilities: legacyCapabilities, CurrentAppliedGeneration: node.AppliedGeneration}
			now := f.rule.CreatedAt.Add(time.Minute)
			node, err = f.store.UpdateHeartbeat(context.Background(), f.nodeID, heartbeat, now, audit.Event{})
			if err != nil {
				t.Fatal(err)
			}
			repaired := f.store.nodeConfigsByNode[f.nodeID][node.DesiredGeneration]
			if node.DesiredGeneration != rejected.Generation+1 || node.BundleCompilerRevision != NodeBundleCompilerRevision || !bytes.Equal(repaired.Config, accepted.Config) {
				t.Fatal("rejected metadata was retained or engine content changed")
			}
			if !bytes.Equal(f.store.nodeConfigsByNode[f.nodeID][rejected.Generation].Config, rejected.Config) {
				t.Fatal("recovery rewrote historical configuration")
			}
			for i := 1; i <= 3; i++ {
				node, err = f.store.UpdateHeartbeat(context.Background(), f.nodeID, heartbeat, now.Add(time.Duration(i)*time.Second), audit.Event{})
				if err != nil || node.DesiredGeneration != repaired.Generation {
					t.Fatal("recovery reissued configuration", err)
				}
			}
			raw, err := f.store.EncodeSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			restored, err := DecodeSnapshot(raw)
			if err != nil {
				t.Fatal(err)
			}
			node, err = restored.UpdateHeartbeat(context.Background(), f.nodeID, heartbeat, now.Add(4*time.Second), audit.Event{})
			if err != nil || node.DesiredGeneration != repaired.Generation {
				t.Fatal("restart reissued recovery", err)
			}
		})
	}
}
