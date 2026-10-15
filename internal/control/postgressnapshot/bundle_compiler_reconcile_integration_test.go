package postgressnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestPostgreSQLCompilerReconcileBypassesTelemetryThrottleAndSurvivesRestart(t *testing.T) {
	s, dsn, _ := protocolHealthFixture(t)
	ctx := context.Background()
	raw, _ := healthSnapshot(t, s)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	var records map[string]map[string]json.RawMessage
	if err := json.Unmarshal(fields["Nodes"], &records); err != nil {
		t.Fatal(err)
	}
	var node nodes.Node
	if err := json.Unmarshal(records["node"]["Node"], &node); err != nil {
		t.Fatal(err)
	}
	var configs map[string]map[int64]generations.NodeConfigGeneration
	if err := json.Unmarshal(fields["NodeConfigsByNode"], &configs); err != nil {
		t.Fatal(err)
	}
	old, exists := configs["node"][node.DesiredGeneration]
	if !exists {
		t.Fatal("fixture is missing the applied node bundle")
	}
	var bundle agentv1.ConfigurationBundle
	if err := json.Unmarshal(old.Config, &bundle); err != nil || len(bundle.Fragments) != 1 {
		t.Fatal("fixture requires one Reality fragment", err)
	}
	var config map[string]any
	if err := json.Unmarshal(bundle.Fragments[0].Config, &config); err != nil {
		t.Fatal(err)
	}
	var identity, privateKey, probeIdentity string
	inbound := config["inbounds"].([]any)[0].(map[string]any)
	clients := inbound["settings"].(map[string]any)["clients"].([]any)
	identity = clients[0].(map[string]any)["id"].(string)
	probeIdentity = clients[1].(map[string]any)["id"].(string)
	privateKey = inbound["streamSettings"].(map[string]any)["realitySettings"].(map[string]any)["privateKey"].(string)
	removed := false
	for _, item := range config["outbounds"].([]any) {
		settings := item.(map[string]any)["settings"].(map[string]any)
		if _, exists := settings["finalRules"]; exists {
			delete(settings, "finalRules")
			removed = true
		}
	}
	if !removed {
		t.Fatal("fixture lacks dedicated probe permission")
	}
	bundle.Fragments[0].Config, _ = json.Marshal(config)
	old.Config, _ = json.Marshal(bundle)
	canonical, err := generations.CanonicalJSON(old.Config)
	if err != nil {
		t.Fatal(err)
	}
	old.Config = canonical
	old.ConfigSHA256 = generations.SHA256Hex(old.Config)
	node.BundleCompilerRevision = 0
	records["node"]["Node"], _ = json.Marshal(node)
	fields["Nodes"], _ = json.Marshal(records)
	fields["NodeConfigsByNode"], _ = json.Marshal(map[string]map[int64]generations.NodeConfigGeneration{"node": {old.Generation: old}})
	raw, _ = json.Marshal(fields)
	if _, err = memoryrepo.DecodeSnapshot(raw); err != nil {
		t.Fatal("invalid legacy snapshot fixture", err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET payload=$1, revision=revision+1 WHERE singleton=true`, raw); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	heartbeat := nodes.Heartbeat{Capabilities: node.Capabilities, CurrentAppliedGeneration: old.Generation}
	s.markHeartbeatPersisted("node", heartbeat, now)
	if s.shouldPersistHeartbeat("node", heartbeat, now.Add(time.Second)) {
		t.Fatal("fixture did not enter telemetry throttle")
	}
	// Concurrent fast-path calls must never return a newly compiled generation
	// without its durable bundle. Exactly one generation survives the upgrade.
	var wait sync.WaitGroup
	failures := make(chan error, 12)
	for range 12 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := s.UpdateHeartbeat(ctx, "node", heartbeat, now.Add(time.Second), audit.Event{}); err != nil {
				failures <- err
			}
		}()
	}
	wait.Wait()
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	current, _, err := s.DesiredNodeConfig(ctx, "node")
	if err != nil || current.Generation != old.Generation+1 || !bytes.Contains(current.Config, []byte(`"finalRules"`)) {
		t.Fatal("legacy bundle was not durably reconciled", err)
	}
	for _, value := range []string{identity, privateKey, probeIdentity} {
		if !bytes.Contains(current.Config, []byte(value)) {
			t.Fatal("reconcile rotated existing customer or probe credentials")
		}
	}
	// The previous applied generation remains hot until a new one is applied;
	// resolve through the historical API, which also handles archived evidence.
	historical, err := s.UsageConfigInput(ctx, "node", old.Generation)
	if err != nil || !bytes.Equal(historical.Config.Config, old.Config) || historical.Config.ConfigSHA256 != old.ConfigSHA256 {
		t.Fatal("legacy configuration history was changed", err)
	}
	_, revision := healthSnapshot(t, s)
	for i := 2; i <= 4; i++ {
		if _, err = s.UpdateHeartbeat(ctx, "node", heartbeat, now.Add(time.Duration(i)*time.Second), audit.Event{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, after := healthSnapshot(t, s); after != revision {
		t.Fatal("ordinary heartbeats rewrote snapshot after compiler reconciliation")
	}
	reopened, err := Open(ctx, dsn, auth.Administrator{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	node, err = reopened.UpdateHeartbeat(ctx, "node", heartbeat, now.Add(5*time.Second), audit.Event{})
	if err != nil || node.BundleCompilerRevision != memoryrepo.NodeBundleCompilerRevision || node.DesiredGeneration != current.Generation {
		t.Fatal("restart lost compiler revision or reissued generation", err)
	}
}
