package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/engine"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// The complete public rule workflow uses only disposable loopback resources.
// One node owns both roles so fixed local statistics ports cannot collide.
func TestReliableUDPAPIThroughRealEngine(t *testing.T) {
	binary := os.Getenv("NYVP_GOST_BINARY")
	if binary == "" {
		t.Skip("set NYVP_GOST_BINARY to the locked reliable UDP engine")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("GOST binary must be absolute")
	}
	for _, transport := range []forwarding.TunnelTransport{forwarding.TunnelQUIC, forwarding.TunnelKCP} {
		t.Run(string(transport), func(t *testing.T) {
			f := newBusinessFixture(t)
			target, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			go func() {
				for {
					c, err := target.Accept()
					if err != nil {
						return
					}
					go func() {
						defer c.Close()
						_ = c.SetDeadline(time.Now().Add(30 * time.Second))
						_, _ = io.Copy(c, c)
					}()
				}
			}()
			spare, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := spare.Addr().(*net.TCPAddr).Port
			secondSpare, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			secondPort := secondSpare.Addr().(*net.TCPAddr).Port
			_ = secondSpare.Close()
			_ = spare.Close()
			businessRequest(t, f, http.MethodPut, "/api/v1/group-networks/"+f.entry.ID, "loopback-entry", groupconfig.Request{ConnectHost: "127.0.0.1", PortStart: min(port, secondPort), PortEnd: max(port, secondPort), AllowDirect: true, AllowedExitGroupIDs: []string{f.exit.ID}, TrafficMultiplier: 1, Revision: 1}, http.StatusOK)
			businessRequest(t, f, http.MethodPut, "/api/v1/group-networks/"+f.exit.ID, "loopback-exit", groupconfig.Request{AllowedEntryGroupIDs: []string{f.entry.ID}, TrafficMultiplier: 1}, http.StatusOK)
			var issued enrollment.IssueResult
			decodeResponse(t, requestJSON(t, f.handler, http.MethodPost, "/api/v1/enrollment-tokens", f.token, map[string]any{"name": "udp-e2e", "expires_in_seconds": 900}, 201), &issued)
			var node agentv1.EnrollmentResponse
			caps := []string{"gost", "gost-reliable-udp-v1"}
			decodeResponse(t, requestJSON(t, f.handler, http.MethodPost, "/api/v1/agent/enroll", "", agentv1.EnrollmentRequest{EnrollmentToken: issued.Token, Hostname: "udp-loopback", Platform: "linux", Architecture: "amd64", AgentVersion: "test", Capabilities: caps}, 201), &node)
			requestJSON(t, f.handler, http.MethodPost, "/api/v1/agent/heartbeat", node.NodeCredential, agentv1.HeartbeatRequest{BootID: "udp-e2e", Hostname: "udp-loopback", Platform: "linux", Architecture: "amd64", AgentVersion: "test", Capabilities: caps}, http.StatusNoContent)
			for _, id := range []string{f.entry.ID, f.exit.ID} {
				requestJSON(t, f.handler, http.MethodPost, "/api/v1/device-groups/"+id+"/members", f.token, map[string]any{"node_id": node.NodeID, "dial_host": "127.0.0.1", "weight": 100, "priority": 0}, 200)
			}
			input := f.ruleRequest()
			input.EgressMode = forwarding.EgressExitGroup
			input.ExitGroupID = f.exit.ID
			input.TunnelTransport = transport
			input.ListenPort = port
			input.Targets = []forwarding.Target{{Host: "127.0.0.1", Port: target.Addr().(*net.TCPAddr).Port}}
			var rule forwarding.Rule
			decodeBusinessEntity(t, businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules", "udp-rule", input, 201), "rule", &rule)
			root := t.TempDir()
			adapter, err := engine.NewGOSTProcessAdapter(engine.GOSTAdapterOptions{StagingDir: filepath.Join(root, "staging"), ActivePath: filepath.Join(root, "active", "config.json"), BinaryPath: binary, AutoStart: true})
			if err != nil {
				t.Fatal(err)
			}
			defer adapter.Close(context.Background())
			if !adapter.SupportsReliableUDP() {
				t.Fatal("real engine lacks locked UDP capability")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
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
				requestJSON(t, f.handler, http.MethodPost, "/api/v1/agent/apply-results", node.NodeCredential, agentv1.ApplyResultRequest{Generation: desired.Generation, ConfigSHA256: desired.ConfigSHA256, Phase: phase, Status: agentv1.ApplyStatusSucceeded, EngineMode: "gost"}, 204)
			}
			var after agentv1.DesiredNodeConfig
			decodeResponse(t, requestJSON(t, f.handler, http.MethodGet, "/api/v1/agent/desired", node.NodeCredential, nil, 200), &after)
			if after.Generation != desired.Generation || after.ConfigSHA256 != desired.ConfigSHA256 {
				t.Fatal("shared-node receipts caused a generation loop")
			}
			c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(5 * time.Second))
			exchange := func(c net.Conn) {
				t.Helper()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				want := "panel-udp-" + string(transport)
				if _, err := io.WriteString(c, want); err != nil {
					t.Fatal(err)
				}
				got := make([]byte, len(want))
				_, err := io.ReadFull(c, got)
				if err != nil || string(got) != want {
					t.Fatalf("real UDP forwarding failed: %v", err)
				}
			}
			exchange(c)
			pid, epoch := adapter.RuntimeMetrics().PID, adapter.CounterEpoch()
			input.ListenPort = secondPort
			input.Name = "second UDP rule"
			businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules", "second-udp-rule", input, 201)
			decodeResponse(t, requestJSON(t, f.handler, http.MethodGet, "/api/v1/agent/desired", node.NodeCredential, nil, 200), &after)
			added, err := adapter.Prepare(ctx, after)
			if err != nil {
				t.Fatal(err)
			}
			if err = adapter.Validate(ctx, added); err != nil {
				t.Fatal(err)
			}
			if err = adapter.CommitValidated(ctx, added); err != nil {
				t.Fatal(err)
			}
			if err = adapter.Verify(ctx, added); err != nil {
				t.Fatal(err)
			}
			if pid == 0 || adapter.RuntimeMetrics().PID != pid || adapter.CounterEpoch() != epoch {
				t.Fatal("pure addition replaced the running engine")
			}
			for i := 0; i < 20; i++ {
				exchange(c)
			}
			newConnection, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(secondPort)), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			exchange(newConnection)
			_ = newConnection.Close()
			for _, phase := range []agentv1.ApplyPhase{agentv1.ApplyPhaseCommit, agentv1.ApplyPhaseVerify} {
				requestJSON(t, f.handler, http.MethodPost, "/api/v1/agent/apply-results", node.NodeCredential, agentv1.ApplyResultRequest{Generation: after.Generation, ConfigSHA256: after.ConfigSHA256, Phase: phase, Status: agentv1.ApplyStatusSucceeded, EngineMode: "gost"}, 204)
			}
			current, err := forwarding.NewService(f.store, nil).Get(context.Background(), rule.ID)
			if err != nil || !current.Deployed {
				t.Fatalf("verified rule not active: %v", err)
			}
			raw, err := f.store.EncodeSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			restored, err := memoryrepo.DecodeSnapshot(raw)
			if err != nil {
				t.Fatal(err)
			}
			persisted, _, err := restored.DesiredNodeConfig(context.Background(), node.NodeID)
			if err != nil || persisted.ConfigSHA256 != after.ConfigSHA256 || !json.Valid(persisted.Config) {
				t.Fatal("verified UDP bundle did not survive persistence")
			}
		})
	}
}
