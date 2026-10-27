//go:build linux

package httpapi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/engine"
	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/rulegroups"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// This opt-in test must itself run under PrivateNetwork=yes (or equivalent).
// It creates two further network namespaces and a veth pair only inside that
// disposable parent. Neither helper can see the other helper's loopback.
func TestReliableUDPTwoNodeAPIThroughRealEngine(t *testing.T) {
	if os.Getenv("HL_PANEL_ISOLATED_RELIABLE_UDP_TEST") != "true" || os.Getenv("NYVP_GOST_BINARY") == "" {
		t.Skip("requires locked GOST and an explicitly isolated Linux network")
	}
	parentNS := udpTestNamespace(t, os.Getpid())
	if parentNS == udpTestNamespace(t, 1) {
		t.Fatal("refusing network setup in the host namespace")
	}
	for _, scenario := range []struct {
		name      string
		transport forwarding.TunnelTransport
		loss      bool
	}{
		{"quic/clean", forwarding.TunnelQUIC, false},
		{"quic/loss_2_percent", forwarding.TunnelQUIC, true},
		{"kcp/clean", forwarding.TunnelKCP, false},
		{"kcp/loss_2_percent", forwarding.TunnelKCP, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			transport := scenario.transport
			entry := startUDPTestNode(t, "entry")
			exit := startUDPTestNode(t, "exit")
			if entry.ready.Namespace == exit.ready.Namespace || entry.ready.Namespace == parentNS || exit.ready.Namespace == parentNS {
				t.Fatal("node network namespaces are not independent")
			}
			udpTestCommand(t, "ip", "link", "add", "hludpe", "type", "veth", "peer", "name", "hludpx")
			for _, item := range []struct {
				node          *udpTestNode
				link, address string
			}{{entry, "hludpe", "10.252.0.1/30"}, {exit, "hludpx", "10.252.0.2/30"}} {
				pid := strconv.Itoa(item.node.cmd.Process.Pid)
				udpTestCommand(t, "ip", "link", "set", item.link, "netns", pid)
				udpTestCommand(t, "nsenter", "--target", pid, "--net", "--", "ip", "addr", "add", item.address, "dev", item.link)
				udpTestCommand(t, "nsenter", "--target", pid, "--net", "--", "ip", "link", "set", item.link, "up")
				if scenario.loss {
					// Only this helper's disposable veth is impaired. Never apply
					// a qdisc in the parent or host network namespace.
					udpTestCommand(t, "nsenter", "--target", pid, "--net", "--", "tc", "qdisc", "add", "dev", item.link, "root", "netem", "loss", "2%")
				}
			}
			f := newBusinessFixture(t)
			businessRequest(t, f, http.MethodPut, "/api/v1/group-networks/"+f.entry.ID, "two-node-entry", groupconfig.Request{ConnectHost: "10.252.0.1", PortStart: 32180, PortEnd: 32181, AllowDirect: true, AllowedExitGroupIDs: []string{f.exit.ID}, TrafficMultiplier: 1, Revision: 1}, http.StatusOK)
			businessRequest(t, f, http.MethodPut, "/api/v1/group-networks/"+f.exit.ID, "two-node-exit", groupconfig.Request{AllowedEntryGroupIDs: []string{f.entry.ID}, TrafficMultiplier: 1}, http.StatusOK)
			entryNode := enrollUDPTestNode(t, f, "entry", f.entry.ID, "10.252.0.1")
			exitNode := enrollUDPTestNode(t, f, "exit", f.exit.ID, "10.252.0.2")
			input := f.ruleRequest()
			input.EgressMode, input.ExitGroupID, input.TunnelTransport = forwarding.EgressExitGroup, f.exit.ID, transport
			input.ListenPort = 32180
			input.Targets = []forwarding.Target{{Host: "127.0.0.1", Port: exit.ready.TargetPort}}
			var first forwarding.Rule
			decodeBusinessEntity(t, businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules", "two-node-first", input, 201), "rule", &first)
			applyUDPTestNode(t, f, exitNode, exit, 1)
			applyUDPTestNode(t, f, entryNode, entry, 1)
			firstConnection := entry.call(t, udpNodeRequest{Operation: "connect", Port: 32180}).Connection
			entry.call(t, udpNodeRequest{Operation: "exchange", Connection: firstConnection, Bytes: 2 * 1024 * 1024})
			entryBefore, exitBefore := entry.call(t, udpNodeRequest{Operation: "stats"}), exit.call(t, udpNodeRequest{Operation: "stats"})

			input.Name, input.ListenPort = "unrelated rule", 32181
			var second forwarding.Rule
			decodeBusinessEntity(t, businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules", "two-node-second", input, 201), "rule", &second)
			applyUDPTestNode(t, f, exitNode, exit, 2)
			applyUDPTestNode(t, f, entryNode, entry, 2)
			assertUDPTestProcessUnchanged(t, entryBefore, entry.call(t, udpNodeRequest{Operation: "stats"}))
			assertUDPTestProcessUnchanged(t, exitBefore, exit.call(t, udpNodeRequest{Operation: "stats"}))
			for i := 0; i < 20; i++ {
				entry.call(t, udpNodeRequest{Operation: "exchange", Connection: firstConnection, Bytes: 1024})
			}
			secondConnection := entry.call(t, udpNodeRequest{Operation: "connect", Port: 32181}).Connection
			entry.call(t, udpNodeRequest{Operation: "exchange", Connection: secondConnection, Bytes: 1024})

			pause := rulegroups.BatchRequest{Operation: rulegroups.BatchPause, RuleIDs: []string{first.ID}, ExpectedRevisions: map[string]int64{first.ID: first.Revision}}
			businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules/batch", "two-node-pause", pause, http.StatusOK)
			applyUDPTestNode(t, f, entryNode, entry, 3)
			applyUDPTestNode(t, f, exitNode, exit, 3)
			assertUDPTestProcessUnchanged(t, entryBefore, entry.call(t, udpNodeRequest{Operation: "stats"}))
			assertUDPTestProcessUnchanged(t, exitBefore, exit.call(t, udpNodeRequest{Operation: "stats"}))
			for i := 0; i < 20; i++ {
				entry.call(t, udpNodeRequest{Operation: "exchange", Connection: secondConnection, Bytes: 1024})
			}
			if response := entry.call(t, udpNodeRequest{Operation: "closed-listener", Port: 32180}); !response.ListenerClosed {
				t.Fatal("paused rule still accepts new connections")
			}
			// A repeated administrator request must not create another generation.
			businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules/batch", "two-node-pause", pause, http.StatusOK)
			for _, node := range []agentv1.EnrollmentResponse{entryNode, exitNode} {
				requestJSON(t, f.handler, http.MethodGet, "/api/v1/agent/desired", node.NodeCredential, nil, http.StatusNoContent)
			}
			raw, err := f.store.EncodeSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			restored, err := memoryrepo.DecodeSnapshot(raw)
			if err != nil {
				t.Fatal(err)
			}
			for _, node := range []agentv1.EnrollmentResponse{entryNode, exitNode} {
				proof, err := restored.RuleNodeDeploymentInput(context.Background(), second.ID, node.NodeID)
				if err != nil || !deploymentreceipts.Evaluate(proof).ReceiptVerified {
					t.Fatal("two-node verified receipt did not survive persistence")
				}
			}
			if scenario.loss {
				entryDrops := udpTestNetemDrops(t, entry, "hludpe")
				exitDrops := udpTestNetemDrops(t, exit, "hludpx")
				if entryDrops == 0 || exitDrops == 0 {
					t.Fatal("loss scenario did not observe actual drops in both directions", entryDrops, exitDrops)
				}
				t.Logf("%s: 2%% configured egress loss per isolated veth; actual drops entry=%d exit=%d; payload and SHA256 verified", transport, entryDrops, exitDrops)
			}
			t.Logf("%s: independent entry/exit; 2 MiB each direction; add and pause retain both PIDs/epochs and unrelated established connection", transport)
		})
	}
}

func enrollUDPTestNode(t *testing.T, f businessFixture, role, groupID, host string) agentv1.EnrollmentResponse {
	t.Helper()
	var issued enrollment.IssueResult
	decodeResponse(t, requestJSON(t, f.handler, http.MethodPost, "/api/v1/enrollment-tokens", f.token, map[string]any{"name": "two-node-" + role, "expires_in_seconds": 900}, 201), &issued)
	var node agentv1.EnrollmentResponse
	caps := []string{"gost", "gost-reliable-udp-v1"}
	decodeResponse(t, requestJSON(t, f.handler, http.MethodPost, "/api/v1/agent/enroll", "", agentv1.EnrollmentRequest{EnrollmentToken: issued.Token, Hostname: role, Platform: "linux", Architecture: "amd64", AgentVersion: "test", Capabilities: caps}, 201), &node)
	requestJSON(t, f.handler, http.MethodPost, "/api/v1/agent/heartbeat", node.NodeCredential, agentv1.HeartbeatRequest{BootID: role, Hostname: role, Platform: "linux", Architecture: "amd64", AgentVersion: "test", Capabilities: caps}, http.StatusNoContent)
	requestJSON(t, f.handler, http.MethodPost, "/api/v1/device-groups/"+groupID+"/members", f.token, map[string]any{"node_id": node.NodeID, "dial_host": host, "weight": 100, "priority": 0}, http.StatusOK)
	return node
}

func applyUDPTestNode(t *testing.T, f businessFixture, node agentv1.EnrollmentResponse, helper *udpTestNode, attempt int) {
	t.Helper()
	var desired agentv1.DesiredNodeConfig
	decodeResponse(t, requestJSON(t, f.handler, http.MethodGet, "/api/v1/agent/desired", node.NodeCredential, nil, http.StatusOK), &desired)
	helper.call(t, udpNodeRequest{Operation: "apply", Desired: &desired})
	for _, phase := range []agentv1.ApplyPhase{agentv1.ApplyPhaseCommit, agentv1.ApplyPhaseVerify} {
		requestJSON(t, f.handler, http.MethodPost, "/api/v1/agent/apply-results", node.NodeCredential, agentv1.ApplyResultRequest{Generation: desired.Generation, AttemptID: fmt.Sprintf("%032d", attempt), ConfigSHA256: desired.ConfigSHA256, Phase: phase, Status: agentv1.ApplyStatusSucceeded, EngineMode: "gost"}, http.StatusNoContent)
	}
}

type udpNodeRequest struct {
	Operation               string
	Desired                 *agentv1.DesiredNodeConfig
	Port, Connection, Bytes int
}

type udpNodeResponse struct {
	Error, Namespace, SHA256    string
	TargetPort, Connection, PID int
	Epoch                       uint64
	ListenerClosed              bool
}

type udpTestNode struct {
	cmd           *exec.Cmd
	input, output *os.File
	encoder       *json.Encoder
	decoder       *json.Decoder
	ready         udpNodeResponse
}

func startUDPTestNode(t *testing.T, role string) *udpTestNode {
	t.Helper()
	requestRead, requestWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	responseRead, responseWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("unshare", "--net", "--", executable, "-test.run=^TestReliableUDPTwoNodeHelper$", "-test.timeout=180s")
	cmd.Env = append(os.Environ(), "HL_PANEL_UDP_NODE_HELPER="+role)
	cmd.ExtraFiles = []*os.File{requestRead, responseWrite}
	var log bytes.Buffer
	cmd.Stdout, cmd.Stderr = &log, &log
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = requestRead.Close()
	_ = responseWrite.Close()
	node := &udpTestNode{cmd: cmd, input: requestWrite, output: responseRead, encoder: json.NewEncoder(requestWrite), decoder: json.NewDecoder(responseRead)}
	t.Cleanup(func() {
		_ = requestWrite.Close()
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("isolated %s helper exit failed: %v; log bytes=%d", role, err, log.Len())
			}
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			t.Errorf("isolated %s helper did not stop", role)
		}
		_ = responseRead.Close()
	})
	node.ready = node.call(t, udpNodeRequest{Operation: "ready"})
	if node.ready.Namespace != udpTestNamespace(t, cmd.Process.Pid) {
		t.Fatal("helper namespace proof mismatch")
	}
	return node
}

func (node *udpTestNode) call(t *testing.T, request udpNodeRequest) udpNodeResponse {
	t.Helper()
	_ = node.input.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_ = node.output.SetReadDeadline(time.Now().Add(30 * time.Second))
	if err := node.encoder.Encode(request); err != nil {
		t.Fatalf("helper %s write: %v", request.Operation, err)
	}
	var response udpNodeResponse
	if err := node.decoder.Decode(&response); err != nil {
		t.Fatalf("helper %s read: %v", request.Operation, err)
	}
	if response.Error != "" {
		t.Fatalf("helper %s failed: %s", request.Operation, response.Error)
	}
	return response
}

func assertUDPTestProcessUnchanged(t *testing.T, before, after udpNodeResponse) {
	t.Helper()
	if before.PID == 0 || before.PID != after.PID || before.Epoch != after.Epoch {
		t.Fatal("unrelated update replaced an isolated node engine")
	}
}

func udpTestNamespace(t *testing.T, pid int) string {
	t.Helper()
	value, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/net", pid))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func udpTestCommand(t *testing.T, name string, args ...string) {
	t.Helper()
	udpTestCommandOutput(t, name, args...)
}

func udpTestCommandOutput(t *testing.T, name string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("isolated network command %s failed: %v: %s", name, err, output)
	}
	return output
}

func udpTestNetemDrops(t *testing.T, node *udpTestNode, link string) uint64 {
	t.Helper()
	if udpTestNamespace(t, node.cmd.Process.Pid) != node.ready.Namespace || node.ready.Namespace == udpTestNamespace(t, 1) {
		t.Fatal("disposable node namespace changed before qdisc inspection")
	}
	output := udpTestCommandOutput(t, "nsenter", "--target", strconv.Itoa(node.cmd.Process.Pid), "--net", "--", "tc", "-j", "-s", "qdisc", "show", "dev", link)
	var qdiscs []struct {
		Kind  string `json:"kind"`
		Drops uint64 `json:"drops"`
	}
	if err := json.Unmarshal(output, &qdiscs); err != nil {
		t.Fatal("invalid isolated qdisc counters", err)
	}
	for _, qdisc := range qdiscs {
		if qdisc.Kind == "netem" {
			return qdisc.Drops
		}
	}
	t.Fatal("isolated veth has no netem qdisc")
	return 0
}

// Fixed pipe operations are test-only; no server shell or arbitrary command API.
func TestReliableUDPTwoNodeHelper(t *testing.T) {
	role := os.Getenv("HL_PANEL_UDP_NODE_HELPER")
	if role != "entry" && role != "exit" {
		t.Skip("isolated child only")
	}
	if os.Getenv("HL_PANEL_ISOLATED_RELIABLE_UDP_TEST") != "true" || udpTestNamespace(t, os.Getpid()) == udpTestNamespace(t, 1) {
		t.Fatal("helper isolation missing")
	}
	udpTestCommand(t, "ip", "link", "set", "lo", "up")
	root := t.TempDir()
	adapter, err := engine.NewGOSTProcessAdapter(engine.GOSTAdapterOptions{StagingDir: filepath.Join(root, "staging"), ActivePath: filepath.Join(root, "active", "config.json"), BinaryPath: os.Getenv("NYVP_GOST_BINARY"), AutoStart: true})
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close(context.Background())
	if !adapter.SupportsReliableUDP() {
		t.Fatal("locked reliable UDP capability missing")
	}
	ready := udpNodeResponse{Namespace: udpTestNamespace(t, os.Getpid())}
	if role == "exit" {
		target, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer target.Close()
		ready.TargetPort = target.Addr().(*net.TCPAddr).Port
		go func() {
			for {
				c, err := target.Accept()
				if err != nil {
					return
				}
				go func() { defer c.Close(); _ = c.SetDeadline(time.Now().Add(90 * time.Second)); _, _ = io.Copy(c, c) }()
			}
		}()
	}
	input, output := os.NewFile(3, "requests"), os.NewFile(4, "responses")
	defer input.Close()
	defer output.Close()
	decoder, encoder := json.NewDecoder(input), json.NewEncoder(output)
	connections := []net.Conn{}
	defer func() {
		for _, c := range connections {
			_ = c.Close()
		}
	}()
	for {
		var request udpNodeRequest
		if err := decoder.Decode(&request); err != nil {
			if !errors.Is(err, io.EOF) {
				t.Error("invalid helper request")
			}
			return
		}
		response := udpNodeResponse{}
		err := func() error {
			switch request.Operation {
			case "ready":
				response = ready
			case "stats":
				response.PID, response.Epoch = adapter.RuntimeMetrics().PID, adapter.CounterEpoch()
			case "apply":
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				if request.Desired == nil {
					return errors.New("desired is required")
				}
				prepared, err := adapter.Prepare(ctx, *request.Desired)
				if err != nil {
					return fmt.Errorf("prepare: %w", err)
				}
				if err = adapter.Validate(ctx, prepared); err != nil {
					return fmt.Errorf("validate: %w", err)
				}
				if err = adapter.CommitValidated(ctx, prepared); err != nil {
					return fmt.Errorf("commit: %w", err)
				}
				if err = adapter.Verify(ctx, prepared); err != nil {
					return fmt.Errorf("verify: %w", err)
				}
			case "connect", "closed-listener":
				c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(request.Port)), time.Second)
				if request.Operation == "closed-listener" {
					response.ListenerClosed = err != nil
					if c != nil {
						_ = c.Close()
					}
					return nil
				}
				if err != nil {
					return err
				}
				response.Connection = len(connections)
				connections = append(connections, c)
			case "exchange":
				if request.Connection < 0 || request.Connection >= len(connections) || request.Bytes <= 0 || request.Bytes > 2*1024*1024 {
					return errors.New("invalid exchange")
				}
				c := connections[request.Connection]
				_ = c.SetDeadline(time.Now().Add(20 * time.Second))
				want := make([]byte, request.Bytes)
				for i := range want {
					want[i] = byte((i*31 + i/251) % 256)
				}
				written := make(chan error, 1)
				go func() { _, err := io.Copy(c, bytes.NewReader(want)); written <- err }()
				got := make([]byte, len(want))
				if _, err := io.ReadFull(c, got); err != nil {
					return err
				}
				if err := <-written; err != nil {
					return err
				}
				if !bytes.Equal(want, got) {
					return errors.New("payload mismatch")
				}
				sum := sha256.Sum256(got)
				response.SHA256 = hex.EncodeToString(sum[:])
			default:
				return errors.New("unknown fixed helper operation")
			}
			return nil
		}()
		if err != nil {
			response.Error = err.Error()
		}
		if err := encoder.Encode(response); err != nil {
			t.Error("helper response failed")
			return
		}
	}
}
