package gost_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/engine"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/forwarding/gostconfig"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestGOSTProcessAdapterForwardsOnLoopback(t *testing.T) {
	binary := verifiedGOST(t)
	target := markerTarget(t, "adapter-forwarded")
	port := unusedLoopbackPort(t)
	rule := forwarding.Rule{
		ID: "adapter-loopback", Name: "adapter loopback", CustomerID: "test-customer", EntryGroupID: "test-entry",
		EgressMode: forwarding.EgressDirect, Protocol: forwarding.ProtocolTCP, ListenPort: port,
		Targets: []forwarding.Target{target}, SelectionPolicy: forwarding.SelectionRoundRobin,
		Status: forwarding.StatusPendingActivation, Revision: 1,
	}
	native, err := gostconfig.CompileDirectTCP([]forwarding.Rule{rule}, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := json.Marshal(agentv1.ConfigurationBundle{SchemaVersion: 1, Fragments: []agentv1.ConfigurationFragment{{
		GroupID: "forwarding-gost/adapter-loopback", GroupRevision: 1, Engine: agentv1.EngineGOST, Config: native,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(bundle)
	root := t.TempDir()
	adapter, err := engine.NewGOSTProcessAdapter(engine.GOSTAdapterOptions{
		StagingDir: filepath.Join(root, "staging"), ActivePath: filepath.Join(root, "active", "config.json"),
		BinaryPath: binary, AutoStart: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close(context.Background()) })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	prepared, err := adapter.Prepare(ctx, agentv1.DesiredNodeConfig{
		Generation: 1, Engine: agentv1.EngineNodeBundle, ConfigSHA256: hex.EncodeToString(hash[:]), Config: bundle,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Validate(ctx, prepared); err != nil {
		t.Fatalf("native GOST configuration validation failed: %v", err)
	}
	if err := adapter.Commit(ctx, prepared); err != nil {
		t.Fatal(err)
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	var response string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := adapter.Verify(ctx, prepared); err != nil {
			t.Fatalf("GOST process exited before readiness: %v", err)
		}
		connection, dialErr := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if dialErr == nil {
			_ = connection.SetDeadline(time.Now().Add(time.Second))
			data, readErr := io.ReadAll(connection)
			_ = connection.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			response = string(data)
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if response != "adapter-forwarded" {
		t.Fatalf("loopback forward response = %q", response)
	}
	if err := adapter.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Verify(ctx, prepared); !errors.Is(err, engine.ErrGOSTNotRunning) {
		t.Fatalf("stopped process still verified: %v", err)
	}
}
