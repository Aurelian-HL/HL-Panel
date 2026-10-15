package xray_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/engine"
	"github.com/hongle/hl-panel/internal/agent/usage"
	"github.com/hongle/hl-panel/internal/agent/usage/xraystats"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// TestXrayProcessAdapterLoopback exercises the same adapter lifecycle used by
// the edge agent against an explicitly pinned local Xray binary. It is skipped
// unless NYVP_XRAY_BINARY points at the verified build, so ordinary unit runs
// never start an engine or touch a non-loopback service.
func TestXrayProcessAdapterLoopback(t *testing.T) {
	binary := xrayBinary(t)
	port := freeLoopbackPort(t)
	runner, err := engine.NewExecXrayRunner(binary)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	adapter, err := engine.NewXrayProcessAdapter(engine.XrayAdapterOptions{
		StagingDir: filepath.Join(root, "staging"),
		ActivePath: filepath.Join(root, "active", "config.json"),
		Runner:     runner,
		AutoStart:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := adapter.Close(context.Background()); err != nil {
			t.Errorf("close adapter: %v", err)
		}
	})
	fragmentConfig, err := json.Marshal(map[string]any{
		"log": map[string]any{"loglevel": "warning", "access": "none"},
		"inbounds": []any{map[string]any{
			"tag": "loopback-socks", "listen": "127.0.0.1", "port": port,
			"protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false},
		}},
		"outbounds": []any{map[string]any{"tag": "direct", "protocol": "freedom"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	desired := adapterDesired(t, 1, []agentv1.ConfigurationFragment{{
		GroupID: "loopback", GroupRevision: 1, Engine: agentv1.EngineXray, Config: fragmentConfig,
	}})
	prepared, err := adapter.Prepare(context.Background(), desired)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Validate(context.Background(), prepared); err != nil {
		t.Fatalf("validate pinned Xray config: %v", err)
	}
	if err := adapter.Commit(context.Background(), prepared); err != nil {
		t.Fatalf("commit pinned Xray config: %v", err)
	}
	if err := adapter.Verify(context.Background(), prepared); err != nil {
		t.Fatalf("verify pinned Xray process: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		connection, dialErr := net.DialTimeout("tcp", localAddress(port), 100*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("pinned Xray adapter process did not open the loopback listener")
}

func TestXrayStatsAPIWithCatchAllBusinessRoute(t *testing.T) {
	runner, err := engine.NewExecXrayRunner(xrayBinary(t))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	adapter, err := engine.NewXrayProcessAdapter(engine.XrayAdapterOptions{
		StagingDir: filepath.Join(root, "staging"), ActivePath: filepath.Join(root, "active", "config.json"),
		Runner: runner, AutoStart: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close(context.Background()) })
	desired := adapterDesired(t, 1, []agentv1.ConfigurationFragment{{
		GroupID: "catch-all", GroupRevision: 1, Engine: agentv1.EngineXray,
		Config: json.RawMessage(`{"log":{"loglevel":"none"},"inbounds":[],"outbounds":[{"tag":"blocked","protocol":"blackhole"}],"routing":{"rules":[{"type":"field","network":"tcp,udp","outboundTag":"blocked"}]}}`),
	}})
	prepared, err := adapter.Prepare(context.Background(), desired)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Commit(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	source, err := xraystats.New("127.0.0.1:10085")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	var lastErr error
	for ctx.Err() == nil {
		attempt, done := context.WithTimeout(ctx, 300*time.Millisecond)
		now := time.Now().UTC()
		_, lastErr = source.CollectUsage(attempt, usage.CollectionWindow{StartedAt: now.Add(-time.Second), EndedAt: now})
		done()
		if lastErr == nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("business catch-all intercepted the managed Stats API: %v", lastErr)
}

func adapterDesired(t *testing.T, generation agentv1.NodeConfigGeneration, fragments []agentv1.ConfigurationFragment) agentv1.DesiredNodeConfig {
	t.Helper()
	bundle, err := json.Marshal(agentv1.ConfigurationBundle{SchemaVersion: engine.BundleSchemaVersion, Fragments: fragments})
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(bundle)
	return agentv1.DesiredNodeConfig{Generation: generation, Engine: agentv1.EngineNodeBundle, ConfigSHA256: hex.EncodeToString(hash[:]), Config: bundle}
}
