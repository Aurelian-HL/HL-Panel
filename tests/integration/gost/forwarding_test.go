package gost_test

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/forwarding/gostconfig"
)

// This test starts only loopback listeners and an explicitly supplied local
// binary. It never needs or accepts production nodes, addresses or credentials.
func TestStructuredRuleThroughRealGOST(t *testing.T) {
	binary := verifiedGOST(t)
	for _, policy := range []forwarding.SelectionPolicy{forwarding.SelectionRoundRobin, forwarding.SelectionRandom, forwarding.SelectionIPHash} {
		t.Run(string(policy), func(t *testing.T) {
			first := markerTarget(t, "target-a")
			second := markerTarget(t, "target-b")
			listenPort := unusedLoopbackPort(t)
			rule := forwarding.Rule{ID: "fwd-real", Name: "loopback verification", CustomerID: "customer-test", EntryGroupID: "entry-test", EgressMode: forwarding.EgressDirect, Protocol: forwarding.ProtocolTCP, ListenPort: listenPort, Targets: []forwarding.Target{first, second}, SelectionPolicy: policy, Status: forwarding.StatusPendingActivation, Revision: 1}
			configuration, err := gostconfig.CompileDirectTCP([]forwarding.Rule{rule}, "127.0.0.1")
			if err != nil {
				t.Fatal(err)
			}
			address := net.JoinHostPort("127.0.0.1", strconv.Itoa(listenPort))
			startGOST(t, binary, configuration, address)
			counts := map[string]int{}
			for index := 0; index < 32; index++ {
				connection, err := net.DialTimeout("tcp", address, time.Second)
				if err != nil {
					t.Fatal(err)
				}
				_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
				response, err := io.ReadAll(connection)
				_ = connection.Close()
				if err != nil {
					t.Fatalf("real forwarding failed: %v", err)
				}
				marker := string(response)
				if marker != "target-a" && marker != "target-b" {
					t.Fatalf("unexpected forwarding response %q", marker)
				}
				counts[marker]++
			}
			if policy != forwarding.SelectionIPHash && (counts["target-a"] == 0 || counts["target-b"] == 0) {
				t.Fatalf("both targets must receive new connections: %v", counts)
			}
			if policy == forwarding.SelectionIPHash && counts["target-a"] != 32 && counts["target-b"] != 32 {
				t.Fatalf("same source IP must remain pinned across new source ports: %v", counts)
			}
			if policy == forwarding.SelectionRoundRobin && (counts["target-a"] != 16 || counts["target-b"] != 16) {
				t.Fatalf("round-robin selection is not balanced: %v", counts)
			}
			t.Logf("real GOST %s target counts: a=%d b=%d", policy, counts["target-a"], counts["target-b"])
		})
	}
}

func verifiedGOST(t *testing.T) string {
	t.Helper()
	binary := os.Getenv("NYVP_GOST_BINARY")
	if binary == "" {
		t.Skip("set NYVP_GOST_BINARY to a verified local GOST 3.3.1 executable")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("NYVP_GOST_BINARY must be absolute")
	}
	info, err := os.Stat(binary)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatal("GOST binary is not a regular executable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	version, err := exec.CommandContext(ctx, binary, "-V").Output()
	if err != nil || !strings.HasPrefix(string(version), "gost 3.3.1 (") {
		t.Fatalf("real engine integration requires GOST 3.3.1: %v", err)
	}
	t.Log(strings.TrimSpace(string(version)))
	return binary
}

func markerTarget(t *testing.T, marker string) forwarding.Target {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			_ = connection.SetDeadline(time.Now().Add(time.Second))
			_, _ = io.WriteString(connection, marker)
			_ = connection.Close()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	return forwarding.Target{Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port}
}

func unusedLoopbackPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func startGOST(t *testing.T, binary string, configuration []byte, address string) {
	t.Helper()
	directory := t.TempDir()
	configPath := filepath.Join(directory, "gost.json")
	if err := os.WriteFile(configPath, configuration, 0600); err != nil {
		t.Fatal(err)
	}
	process := exec.Command(binary, "-C", configPath)
	process.Dir = directory
	var diagnostic bytes.Buffer
	process.Stdout = &diagnostic
	process.Stderr = &diagnostic
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- process.Wait() }()
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = process.Process.Kill()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("GOST process did not stop")
			}
		}
		if t.Failed() {
			for _, marker := range []string{"unmarshal", "invalid", "error", "refused", "timeout"} {
				if strings.Contains(diagnostic.String(), marker) {
					t.Logf("GOST diagnostic marker: %s", marker)
				}
			}
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			_ = connection.SetDeadline(time.Now().Add(time.Second))
			_, _ = io.ReadAll(connection)
			_ = connection.Close()
			return
		}
		select {
		case err := <-done:
			stopped = true
			t.Fatalf("GOST exited before readiness: %v", err)
		default:
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("GOST listener did not become ready")
}
