package gost_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/engine"
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

// TestStructuredRuleThroughRealGOSTUDP exercises the ordinary native UDP
// DIRECT path with loopback-only endpoints. It deliberately does not use the
// research reliable-UDP overlays: this is the customer-facing datagram
// forwarding contract implemented by CompileDirectUDP.
func TestStructuredRuleThroughRealGOSTUDP(t *testing.T) {
	binary := verifiedGOST(t)
	target := udpEchoTarget(t)
	listenPort := unusedUDPPort(t)
	rule := forwarding.Rule{
		ID: "fwd-real-udp", Name: "loopback UDP verification", CustomerID: "customer-test", EntryGroupID: "entry-test",
		IngressProtocol: forwarding.IngressUDP, EgressMode: forwarding.EgressDirect, Protocol: forwarding.ProtocolUDP,
		ListenPort: listenPort, Targets: []forwarding.Target{target}, SelectionPolicy: forwarding.SelectionRoundRobin,
		Status: forwarding.StatusPendingActivation, Revision: 1,
	}
	configuration, err := gostconfig.CompileDirectUDP([]forwarding.Rule{rule}, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(listenPort))
	client := startGOSTUDP(t, binary, configuration, address, target)
	defer client.Close()
	for index := 0; index < 64; index++ {
		payload := []byte(fmt.Sprintf("udp-payload-%03d", index))
		if _, err := client.Write(payload); err != nil {
			t.Fatalf("write datagram %d: %v", index, err)
		}
		_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
		response := make([]byte, 2048)
		n, _, err := client.ReadFromUDP(response)
		if err != nil {
			t.Fatalf("read datagram %d: %v", index, err)
		}
		if string(response[:n]) != string(payload) {
			t.Fatalf("datagram %d response = %q, want %q", index, response[:n], payload)
		}
	}
}

func verifiedGOST(t *testing.T) string {
	t.Helper()
	binary := os.Getenv("NYVP_GOST_BINARY")
	if binary == "" {
		t.Skip("set NYVP_GOST_BINARY to a verified local supported pinned GOST executable")
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
	if err != nil || !engine.IsSupportedGOSTVersion(string(version)) {
		t.Fatalf("real engine integration requires a supported pinned GOST version: %v", err)
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

func udpEchoTarget(t *testing.T) forwarding.Target {
	t.Helper()
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buffer := make([]byte, 2048)
		for {
			n, address, err := listener.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			if _, err := listener.WriteToUDP(buffer[:n], address); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	return forwarding.Target{Host: "127.0.0.1", Port: listener.LocalAddr().(*net.UDPAddr).Port}
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

func unusedUDPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	port := listener.LocalAddr().(*net.UDPAddr).Port
	_ = listener.Close()
	return port
}

func mustResolveUDPAddr(t *testing.T, address string) *net.UDPAddr {
	t.Helper()
	resolved, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
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

func startGOSTUDP(t *testing.T, binary string, configuration []byte, address string, target forwarding.Target) *net.UDPConn {
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
				t.Error("GOST UDP process did not stop")
			}
		}
		if t.Failed() {
			for _, marker := range []string{"unmarshal", "invalid", "error", "refused", "timeout"} {
				if strings.Contains(diagnostic.String(), marker) {
					t.Logf("GOST UDP diagnostic marker: %s", marker)
				}
			}
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		client, err := net.DialUDP("udp", nil, mustResolveUDPAddr(t, address))
		if err == nil {
			probe := []byte("gost-udp-ready")
			_ = client.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
			_, writeErr := client.Write(probe)
			response := make([]byte, 256)
			n, _, readErr := client.ReadFromUDP(response)
			if writeErr == nil && readErr == nil && string(response[:n]) == string(probe) {
				_ = client.SetReadDeadline(time.Time{})
				return client
			}
			_ = client.Close()
		}
		select {
		case err := <-done:
			stopped = true
			t.Fatalf("GOST exited before UDP readiness: %v (target %s)", err, net.JoinHostPort(target.Host, strconv.Itoa(target.Port)))
		default:
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("GOST UDP listener did not become ready")
	return nil
}
