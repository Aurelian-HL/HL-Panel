package memoryrepo

import (
	"context"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	"testing"
	"time"
)

func TestUsageCapabilityCompilesBillingSnapshotOnceAndPreservesOldBundle(t *testing.T) {
	f := newVLESSBundleFixture(t, true)
	now := time.Now().UTC()
	if _, _, err := f.store.compileNodeConfigLocked(f.nodeID, now); err != nil {
		t.Fatal(err)
	}
	oldGeneration, oldRaw, oldBundle := desiredVLESSBundle(t, f)
	if len(oldBundle.Fragments) != 1 || oldBundle.Fragments[0].Usage != nil {
		t.Fatal("legacy node received incompatible metadata")
	}
	network := f.store.groupNetworks[f.rule.EntryGroupID]
	network.TrafficMultiplier = 1.5
	f.store.groupNetworks[f.rule.EntryGroupID] = network
	heartbeat := nodes.Heartbeat{Capabilities: []string{"xray", "vless-reality", agentv1.CapabilityUsageGeneration}}
	if _, err := f.store.UpdateHeartbeat(context.Background(), f.nodeID, heartbeat, now.Add(time.Minute), vlessMutationEvent(t, now, "node.heartbeat", "node", f.nodeID)); err != nil {
		t.Fatal(err)
	}
	generation, _, bundle := desiredVLESSBundle(t, f)
	if generation != oldGeneration+1 || len(bundle.Fragments) != 1 || bundle.Fragments[0].Usage == nil {
		t.Fatal("capability upgrade did not compile metadata")
	}
	m := bundle.Fragments[0].Usage
	if m.RuleID != f.rule.ID || m.CustomerID != f.rule.CustomerID || m.EntryMultiplierMicros != 1_500_000 {
		t.Fatalf("billing snapshot: %+v", m)
	}
	if _, err := f.store.UpdateHeartbeat(context.Background(), f.nodeID, heartbeat, now.Add(2*time.Minute), vlessMutationEvent(t, now, "node.heartbeat", "node", f.nodeID)); err != nil {
		t.Fatal(err)
	}
	if f.store.nodes[f.nodeID].DesiredGeneration != generation {
		t.Fatal("unchanged capability heartbeat recompiled bundle")
	}
	old, err := f.store.UsageConfigInput(context.Background(), f.nodeID, oldGeneration)
	if err != nil || string(old.Config.Config) != oldRaw {
		t.Fatal("old bundle was overwritten")
	}
	// Snapshot persistence must retain historical evidence and defensive copies.
	snapshot, err := f.store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	copy, err := restored.UsageConfigInput(context.Background(), f.nodeID, generation)
	if err != nil {
		t.Fatal(err)
	}
	copy.Config.Config[0] = 'x'
	unchanged, err := restored.UsageConfigInput(context.Background(), f.nodeID, generation)
	if err != nil || unchanged.Config.Config[0] != '{' {
		t.Fatal("historical query mutated durable bundle")
	}
}
