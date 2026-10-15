package memoryrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func legacyProbeBundle(t *testing.T) (vlessBundleFixture, generations.NodeConfigGeneration) {
	t.Helper()
	f := newVLESSBundleFixture(t, true)
	config, _, err := f.store.compileNodeConfigLocked(f.nodeID, f.rule.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	var bundle agentv1.ConfigurationBundle
	if err := json.Unmarshal(config.Config, &bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Fragments) != 1 {
		t.Fatal("expected one executable Reality fragment")
	}
	var fragment map[string]any
	if err := json.Unmarshal(bundle.Fragments[0].Config, &fragment); err != nil {
		t.Fatal(err)
	}
	removed := false
	for _, outbound := range fragment["outbounds"].([]any) {
		settings := outbound.(map[string]any)["settings"].(map[string]any)
		if _, ok := settings["finalRules"]; ok {
			delete(settings, "finalRules")
			removed = true
		}
	}
	if !removed {
		t.Fatal("fixture lacks dedicated probe permission")
	}
	bundle.Fragments[0].Config, _ = json.Marshal(fragment)
	config.Config, _ = json.Marshal(bundle)
	config.Config, err = generations.CanonicalJSON(config.Config)
	if err != nil {
		t.Fatal(err)
	}
	config.ConfigSHA256 = generations.SHA256Hex(config.Config)
	f.store.nodeConfigsByNode[f.nodeID][config.Generation] = config
	node := f.store.nodes[f.nodeID]
	node.BundleCompilerRevision = 0
	f.store.nodes[f.nodeID] = node
	return f, config
}

func TestLegacyProbeBundleReconcilesOnceAndPreservesHistoryAndSecrets(t *testing.T) {
	f, old := legacyProbeBundle(t)
	ctx, now := context.Background(), f.rule.CreatedAt.Add(time.Minute)
	heartbeat := nodes.Heartbeat{Capabilities: f.store.nodes[f.nodeID].Capabilities}
	node, err := f.store.UpdateHeartbeat(ctx, f.nodeID, heartbeat, now, audit.Event{})
	if err != nil || node.BundleCompilerRevision != NodeBundleCompilerRevision || node.DesiredGeneration != old.Generation+1 {
		t.Fatalf("legacy reconcile: generation=%d revision=%d err=%v", node.DesiredGeneration, node.BundleCompilerRevision, err)
	}
	current := f.store.nodeConfigsByNode[f.nodeID][node.DesiredGeneration]
	if !bytes.Contains(current.Config, []byte(`"finalRules"`)) ||
		!bytes.Contains(current.Config, []byte(f.credential)) || !bytes.Contains(current.Config, []byte(f.privateKey)) {
		t.Fatal("reconcile omitted permission or changed customer identity/key")
	}
	if previous := f.store.nodeConfigsByNode[f.nodeID][old.Generation]; !bytes.Equal(previous.Config, old.Config) || previous.ConfigSHA256 != old.ConfigSHA256 {
		t.Fatal("reconcile rewrote historical configuration")
	}
	for i := 1; i <= 3; i++ {
		if _, err := f.store.UpdateHeartbeat(ctx, f.nodeID, heartbeat, now.Add(time.Duration(i)*time.Second), audit.Event{}); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.store.nodeConfigsByNode[f.nodeID]) != 2 || f.store.nodes[f.nodeID].DesiredGeneration != current.Generation {
		t.Fatal("ordinary heartbeats reissued reconciled bundle")
	}
	raw, err := f.store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	node, err = restored.UpdateHeartbeat(ctx, f.nodeID, heartbeat, now.Add(4*time.Second), audit.Event{})
	if err != nil || node.BundleCompilerRevision != NodeBundleCompilerRevision || node.DesiredGeneration != current.Generation {
		t.Fatal("restart lost successful compiler reconciliation", err)
	}
}

func TestReadOnlyHeartbeatNeverPublishesUnpersistedCompilerGeneration(t *testing.T) {
	f, old := legacyProbeBundle(t)
	node, err := f.store.UpdateHeartbeatTelemetry(context.Background(), f.nodeID,
		nodes.Heartbeat{Capabilities: f.store.nodes[f.nodeID].Capabilities, Resources: map[string]any{"sample": "new"}},
		f.rule.CreatedAt.Add(time.Minute), audit.Event{})
	if err != nil || node.DesiredGeneration != old.Generation || node.BundleCompilerRevision != 0 || node.Resources["sample"] != "new" {
		t.Fatal("telemetry fast path published a non-durable generation", err)
	}
	if len(f.store.nodeConfigsByNode[f.nodeID]) != 1 {
		t.Fatal("telemetry fast path compiled configuration")
	}
}

func TestUnchangedCompilerReconcileMarksRevisionWithoutReissuingBundle(t *testing.T) {
	f := automaticMemberFixture(t)
	old := f.store.nodes[f.nodeID].DesiredGeneration
	node := f.store.nodes[f.nodeID]
	node.BundleCompilerRevision = 0
	f.store.nodes[f.nodeID] = node
	heartbeat := nodes.Heartbeat{Capabilities: node.Capabilities}
	node, err := f.store.UpdateHeartbeat(context.Background(), f.nodeID, heartbeat, f.rule.CreatedAt.Add(time.Minute), audit.Event{})
	if err != nil || node.BundleCompilerRevision != NodeBundleCompilerRevision || node.DesiredGeneration != old {
		t.Fatal("unchanged bundle should mark compiler revision without deployment", err)
	}
	// A subsequent malformed historical revision is a sentinel: an ordinary
	// heartbeat must not invoke the compiler again after the persisted marker.
	f.store.revisionsByGroup[f.rule.EntryGroupID] = []generations.GroupRevision{{Engine: agentv1.EngineGOST, Config: json.RawMessage(`{broken`)}}
	if _, err := f.store.UpdateHeartbeat(context.Background(), f.nodeID, heartbeat, f.rule.CreatedAt.Add(2*time.Minute), audit.Event{}); err != nil {
		t.Fatal("ordinary heartbeat recompiled after successful reconciliation", err)
	}
}

func TestFailedCompilerReconcileKeepsOldBundleAndRetriesAfterRepair(t *testing.T) {
	f, old := legacyProbeBundle(t)
	now := f.rule.CreatedAt.Add(time.Minute)
	heartbeat := nodes.Heartbeat{Capabilities: f.store.nodes[f.nodeID].Capabilities}
	f.store.revisionsByGroup[f.rule.EntryGroupID] = []generations.GroupRevision{{Engine: agentv1.EngineGOST, Config: json.RawMessage(`{broken`)}}
	if _, err := f.store.UpdateHeartbeat(context.Background(), f.nodeID, heartbeat, now, audit.Event{}); err == nil {
		t.Fatal("malformed historical fragment did not fail reconciliation")
	}
	if f.store.nodes[f.nodeID].BundleCompilerRevision != 0 || f.store.nodes[f.nodeID].DesiredGeneration != old.Generation || len(f.store.nodeConfigsByNode[f.nodeID]) != 1 {
		t.Fatal("failed reconcile advanced marker or replaced prior generation")
	}
	delete(f.store.revisionsByGroup, f.rule.EntryGroupID)
	node, err := f.store.UpdateHeartbeat(context.Background(), f.nodeID, heartbeat, now.Add(time.Second), audit.Event{})
	if err != nil || node.DesiredGeneration != old.Generation+1 || node.BundleCompilerRevision != NodeBundleCompilerRevision {
		t.Fatal("repaired compiler reconciliation did not retry", err)
	}
}
