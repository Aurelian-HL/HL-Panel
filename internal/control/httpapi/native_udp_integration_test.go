package httpapi_test

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/config"
	"github.com/hongle/hl-panel/internal/agent/engine"
	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// Exercise the public rule API, capability admission, node desired bundle,
// real native UDP sockets, apply receipts and persistence on loopback only.
func TestNativeUDPAPIThroughRealEngine(t *testing.T) {
	binary := os.Getenv("NYVP_GOST_BINARY")
	if binary == "" {
		t.Skip("set NYVP_GOST_BINARY to the supported release engine")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("GOST binary must be absolute")
	}
	f := newBusinessFixture(t)
	target, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		buffer := make([]byte, 2048)
		for {
			n, peer, err := target.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			_, _ = target.WriteToUDP(buffer[:n], peer)
		}
	}()
	reserve := func() *net.UDPConn {
		t.Helper()
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	first, second := reserve(), reserve()
	port, secondPort := first.LocalAddr().(*net.UDPAddr).Port, second.LocalAddr().(*net.UDPAddr).Port
	_ = first.Close()
	_ = second.Close()
	businessRequest(t, f, http.MethodPut, "/api/v1/group-networks/"+f.entry.ID, "native-network", groupconfig.Request{
		ConnectHost: "127.0.0.1", PortStart: min(port, secondPort), PortEnd: max(port, secondPort), AllowDirect: true, TrafficMultiplier: 1, Revision: 1,
	}, http.StatusOK)
	var issued enrollment.IssueResult
	decodeResponse(t, requestJSON(t, f.handler, http.MethodPost, "/api/v1/enrollment-tokens", f.token, map[string]any{"name": "native-udp", "expires_in_seconds": 900}, 201), &issued)
	var node agentv1.EnrollmentResponse
	caps := []string{"gost", config.CapabilityGOSTTCPDirect}
	decodeResponse(t, requestJSON(t, f.handler, http.MethodPost, "/api/v1/agent/enroll", "", agentv1.EnrollmentRequest{
		EnrollmentToken: issued.Token, Hostname: "native-udp-loopback", Platform: "linux", Architecture: "amd64", AgentVersion: "test", Capabilities: caps,
	}, 201), &node)
	requestJSON(t, f.handler, http.MethodPost, "/api/v1/agent/heartbeat", node.NodeCredential, agentv1.HeartbeatRequest{
		BootID: "native-udp", Hostname: "native-udp-loopback", Platform: "linux", Architecture: "amd64", AgentVersion: "test", Capabilities: caps,
	}, 204)
	requestJSON(t, f.handler, http.MethodPost, "/api/v1/device-groups/"+f.entry.ID+"/members", f.token,
		map[string]any{"node_id": node.NodeID, "dial_host": "127.0.0.1", "weight": 100, "priority": 0}, 200)
	input := f.ruleRequest()
	input.IngressProtocol, input.Protocol, input.EgressMode = forwarding.IngressUDP, forwarding.ProtocolUDP, forwarding.EgressDirect
	input.ExitGroupID, input.TunnelTransport = "", ""
	input.ListenPort = port
	input.Targets = []forwarding.Target{{Host: "127.0.0.1", Port: target.LocalAddr().(*net.UDPAddr).Port}}
	var rule forwarding.Rule
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules", "native-first", input, 201), "rule", &rule)
	if rule.Deployed {
		t.Fatal("new UDP rule reported active before engine receipts")
	}
	legacy, err := forwarding.NewService(f.store, nil).Get(context.Background(), rule.ID)
	if err != nil || legacy.ActivationReason != forwarding.ActivationReasonUDPUnsupported {
		t.Fatalf("legacy agent was not gated by its UDP capability: %v", err)
	}
	// A node upgrade must activate saved UDP rules on its first capability
	// heartbeat; an operator must not need to edit each existing rule again.
	caps = append(caps, config.CapabilityGOSTUDPDirect)
	heartbeat := agentv1.HeartbeatRequest{
		BootID: "native-udp", Hostname: "native-udp-loopback", Platform: "linux", Architecture: "amd64", AgentVersion: "test", Capabilities: caps,
	}
	requestJSON(t, f.handler, http.MethodPost, "/api/v1/agent/heartbeat", node.NodeCredential, heartbeat, 204)
	root := t.TempDir()
	adapter, err := engine.NewGOSTProcessAdapter(engine.GOSTAdapterOptions{
		StagingDir: filepath.Join(root, "staging"), ActivePath: filepath.Join(root, "active", "config.json"), BinaryPath: binary, AutoStart: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	apply := func(attempt int) agentv1.DesiredNodeConfig {
		t.Helper()
		var desired agentv1.DesiredNodeConfig
		decodeResponse(t, requestJSON(t, f.handler, http.MethodGet, "/api/v1/agent/desired", node.NodeCredential, nil, 200), &desired)
		prepared, err := adapter.Prepare(ctx, desired)
		if err != nil {
			t.Fatal(err)
		}
		if err = adapter.Validate(ctx, prepared); err != nil {
			t.Fatal(err)
		}
		if err = adapter.CommitValidated(ctx, prepared); err != nil {
			t.Fatal(err)
		}
		if err = adapter.Verify(ctx, prepared); err != nil {
			t.Fatal(err)
		}
		for _, phase := range []agentv1.ApplyPhase{agentv1.ApplyPhaseCommit, agentv1.ApplyPhaseVerify} {
			requestJSON(t, f.handler, http.MethodPost, "/api/v1/agent/apply-results", node.NodeCredential, agentv1.ApplyResultRequest{
				Generation: desired.Generation, AttemptID: fmt.Sprintf("%032d", attempt), ConfigSHA256: desired.ConfigSHA256, Phase: phase, Status: agentv1.ApplyStatusSucceeded, EngineMode: "gost",
			}, 204)
		}
		return desired
	}
	desired := apply(1)
	client, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	exchange := func(c *net.UDPConn, sequence int) {
		t.Helper()
		payload := []byte(fmt.Sprintf("native-udp-%03d", sequence))
		if err := c.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Write(payload); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, 2048)
		n, err := c.Read(got)
		if err != nil || !bytes.Equal(got[:n], payload) {
			t.Fatalf("UDP datagram %d failed: %v", sequence, err)
		}
	}
	exchange(client, 0)
	pid, epoch := adapter.RuntimeMetrics().PID, adapter.CounterEpoch()
	input.Name, input.ListenPort = "native-second", secondPort
	businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules", "native-second", input, 201)
	previous, err := forwarding.NewService(f.store, nil).Get(ctx, rule.ID)
	if err != nil || !previous.Deployed {
		t.Fatalf("unrelated pending UDP addition revoked the applied rule: %v", err)
	}
	desired = apply(2)
	if pid == 0 || adapter.RuntimeMetrics().PID != pid || adapter.CounterEpoch() != epoch {
		t.Fatal("adding a UDP rule replaced the running engine or counter epoch")
	}
	for i := 1; i <= 64; i++ {
		exchange(client, i)
	}
	other, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: secondPort})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	exchange(other, 65)
	requestJSON(t, f.handler, http.MethodPost, "/api/v1/agent/heartbeat", node.NodeCredential, heartbeat, 204)
	requestJSON(t, f.handler, http.MethodGet, "/api/v1/agent/desired", node.NodeCredential, nil, http.StatusNoContent)
	current, err := forwarding.NewService(f.store, nil).Get(ctx, rule.ID)
	if err != nil || !current.Deployed {
		t.Fatalf("verified native UDP rule not active: %v", err)
	}
	raw, err := f.store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := memoryrepo.DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := restored.RuleNodeDeploymentInput(ctx, rule.ID, node.NodeID)
	if err != nil || !deploymentreceipts.Evaluate(receipt).ReceiptVerified || receipt.Node.DesiredGeneration != int64(desired.Generation) {
		t.Fatalf("native UDP receipt did not survive persistence: %v", err)
	}
}
