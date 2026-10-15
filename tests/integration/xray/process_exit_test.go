package xray_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/engine"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// A dead process must stay dead across the heartbeat, metrics and reconciler
// observers. Otherwise one observer consumes the exit notification and later
// observers incorrectly attest a running engine, preventing recovery.
func TestXrayProcessAdapterDoesNotResurrectExitedEngine(t *testing.T) {
	runner, err := engine.NewExecXrayRunner(xrayBinary(t))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	adapter, err := engine.NewXrayProcessAdapter(engine.XrayAdapterOptions{
		StagingDir: filepath.Join(root, "staging"),
		ActivePath: filepath.Join(root, "active", "config.json"),
		Runner:     runner, AutoStart: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := adapter.Close(ctx); err != nil {
			t.Errorf("close adapter: %v", err)
		}
	})
	port := freeLoopbackPort(t)
	fragment := json.RawMessage(`{"log":{"loglevel":"none"},"inbounds":[],"outbounds":[{"protocol":"freedom"}]}`)
	var native map[string]any
	if err := json.Unmarshal(fragment, &native); err != nil {
		t.Fatal(err)
	}
	native["inbounds"] = []any{map[string]any{
		"listen": "127.0.0.1", "port": port, "protocol": "socks",
		"settings": map[string]any{"auth": "noauth", "udp": false},
	}}
	fragment, err = json.Marshal(native)
	if err != nil {
		t.Fatal(err)
	}
	desired := adapterDesired(t, 1, []agentv1.ConfigurationFragment{{
		GroupID: "loopback-exit", GroupRevision: 1, Engine: agentv1.EngineXray, Config: fragment,
	}})
	prepared, err := adapter.Prepare(context.Background(), desired)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Commit(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", localAddress(port), 100*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			ready = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatal("loopback engine did not become ready")
	}
	process, err := os.FindProcess(adapter.RuntimeMetrics().PID)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Kill(); err != nil {
		_ = process.Release()
		t.Fatal(err)
	}
	_ = process.Release()
	deadline = time.Now().Add(3 * time.Second)
	for adapter.ActiveEngineMode() != "" {
		if time.Now().After(deadline) {
			t.Fatal("adapter did not observe engine exit")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for observation := 0; observation < 8; observation++ {
		if err := adapter.Verify(context.Background(), prepared); !errors.Is(err, engine.ErrXrayNotRunning) {
			t.Fatalf("observation %d attested an exited engine: %v", observation, err)
		}
		if adapter.ActiveEngineMode() != "" || adapter.RuntimeMetrics().PID != 0 {
			t.Fatalf("observation %d resurrected an exited engine", observation)
		}
	}
	// The persisted configuration can still be started again after a real exit.
	if err := adapter.Commit(context.Background(), prepared); err != nil {
		t.Fatalf("recover previous configuration: %v", err)
	}
	if err := adapter.Verify(context.Background(), prepared); err != nil {
		t.Fatalf("verify recovered engine: %v", err)
	}
}
