package runtime

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/config"
	usageagent "github.com/hongle/hl-panel/internal/agent/usage"
	"github.com/hongle/hl-panel/internal/agent/usage/xraystatsproto"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Only loopback servers and the pinned real Xray binary are used. Exercise the
// long-lived loop, not a simulated process flag or a repeated desired response.
func TestRealEngineRecoveryWithoutNewDesiredAndDuringControlOutage(t *testing.T) {
	binary := os.Getenv("NYVP_XRAY_BINARY")
	if binary == "" {
		t.Skip("set NYVP_XRAY_BINARY for real loopback engine recovery")
	}
	reservePort := func() int {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		_ = listener.Close()
		return port
	}
	port := reservePort()
	native := json.RawMessage(fmt.Sprintf(`{"log":{"loglevel":"none"},"inbounds":[{"tag":"loopback","listen":"127.0.0.1","port":%d,"protocol":"socks","settings":{"auth":"noauth","udp":false}}],"outbounds":[{"protocol":"freedom"}]}`, port))
	bundle, err := json.Marshal(agentv1.ConfigurationBundle{SchemaVersion: 1, Fragments: []agentv1.ConfigurationFragment{{GroupID: "loopback", GroupRevision: 1, Engine: agentv1.EngineXray, Config: native}}})
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(bundle)
	desired := agentv1.DesiredNodeConfig{Generation: 1, Engine: agentv1.EngineNodeBundle, ConfigSHA256: hex.EncodeToString(hash[:]), Config: bundle}
	var offered, offline atomic.Bool
	var command atomic.Int32
	offered.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if offline.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/agent/enroll":
			_ = json.NewEncoder(w).Encode(agentv1.EnrollmentResponse{NodeID: "loopback-node", NodeCredential: "loopback-credential"})
		case "/api/v1/agent/desired":
			if offered.Load() {
				_ = json.NewEncoder(w).Encode(desired)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/agent/desired/watch":
			http.NotFound(w, r) // Exercise old control-plane polling compatibility.
		case "/api/v1/agent/control":
			kind := command.Load()
			if kind == 0 {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			name := agentv1.ControlStop
			if kind == 2 {
				name = agentv1.ControlRestart
			}
			_ = json.NewEncoder(w).Encode(agentv1.ControlCommandEnvelope{ID: fmt.Sprint(kind), Command: name})
		case "/api/v1/agent/control-results":
			var result agentv1.ControlCommandResultRequest
			if err := json.NewDecoder(r.Body).Decode(&result); err != nil || result.Status != "succeeded" {
				t.Errorf("control result = %v, %v", result.Status, err)
			}
			command.Store(0)
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/agent/heartbeat", "/api/v1/agent/apply-results":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("NYVP_ENROLLMENT_TOKEN", "loopback-token")
	// Integration packages run concurrently. Each test needs its own stats/API
	// listener; sharing the installer's fixed port can terminate the real engine.
	apiAddress := fmt.Sprintf("127.0.0.1:%d", reservePort())
	cfg := config.Config{
		ControlPlaneURL: server.URL, AllowInsecureLoopback: true,
		DataDir: filepath.Join(t.TempDir(), "agent"), Hostname: "loopback",
		AgentVersion: "test", EnrollmentTokenEnv: "NYVP_ENROLLMENT_TOKEN",
		EngineMode: config.EngineModeXray, XrayBinaryPath: binary, XrayAutoStart: true,
		XrayAPIAddress: apiAddress, XrayStatsAPIAddress: apiAddress,
		ProtocolProbeEchoPort: reservePort(), HeartbeatInterval: config.Duration(time.Second),
		DesiredPollInterval: config.Duration(time.Second), RequestTimeout: config.Duration(time.Second),
		Backoff: config.BackoffConfig{Initial: config.Duration(time.Second), Maximum: config.Duration(time.Second), Multiplier: 1},
	}
	makeAgent := func() *Agent {
		a, err := New(cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = a.Close(context.Background()) })
		if err := a.Bootstrap(context.Background()); err != nil {
			t.Fatal(err)
		}
		return a
	}
	a := makeAgent()
	trafficAgent := a
	if applied, err := a.DesiredOnce(context.Background()); err != nil || !applied {
		t.Fatalf("initial apply = %v, %v", applied, err)
	}
	offered.Store(false)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("loopback-ok")) }))
	defer target.Close()
	statsConn, err := grpc.NewClient(apiAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer statsConn.Close()
	statsClient := xraystatsproto.NewStatsServiceClient(statsConn)
	assertTraffic := func() {
		t.Helper()
		// Starting the owned process precedes binding its listener. Wait only
		// for startup readiness; protocol and traffic errors still fail below.
		var c net.Conn
		var err error
		deadline := time.Now().Add(3 * time.Second)
		for {
			c, err = net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("listener not ready: %v; process PID=%d, mode=%q", err, trafficAgent.xrayAdapter.RuntimeMetrics().PID, trafficAgent.activeEngineMode())
			}
			time.Sleep(20 * time.Millisecond)
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		_, _ = c.Write([]byte{5, 1, 0})
		var greeting [2]byte
		if _, err := io.ReadFull(c, greeting[:]); err != nil || greeting != [2]byte{5, 0} {
			t.Fatalf("SOCKS greeting = %v, %v", greeting, err)
		}
		p := target.Listener.Addr().(*net.TCPAddr).Port
		_, _ = c.Write([]byte{5, 1, 0, 1, 127, 0, 0, 1, byte(p >> 8), byte(p)})
		var reply [10]byte
		if _, err := io.ReadFull(c, reply[:]); err != nil || reply[1] != 0 {
			t.Fatalf("SOCKS connect = %v, %v", reply[1], err)
		}
		_, _ = io.WriteString(c, "GET / HTTP/1.1\r\nHost: loopback\r\nConnection: close\r\n\r\n")
		response, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != 200 || string(body) != "loopback-ok" {
			t.Fatalf("recovered real traffic = %d, %q, %v", response.StatusCode, body, err)
		}
		statsCtx, statsCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer statsCancel()
		sample, err := statsClient.QueryStats(statsCtx, &xraystatsproto.QueryStatsRequest{Pattern: "inbound>>>loopback>>>traffic>>>"}, grpc.WaitForReady(true))
		if err != nil {
			t.Fatalf("configured stats address is not usable after real traffic: %v", err)
		}
		var total int64
		for _, stat := range sample.Stat {
			total += stat.Value
		}
		if total <= 0 {
			t.Fatal("real loopback traffic was not counted")
		}
	}
	assertTraffic()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	for _, outage := range []bool{false, true} {
		offline.Store(outage)
		pid := a.xrayAdapter.RuntimeMetrics().PID
		process, err := os.FindProcess(pid)
		if err != nil {
			t.Fatal(err)
		}
		if err := process.Kill(); err != nil {
			t.Fatal(err)
		}
		_ = process.Release()
		deadline := time.Now().Add(8 * time.Second)
		for {
			recovered := a.xrayAdapter.RuntimeMetrics().PID
			if recovered != 0 && recovered != pid {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("engine stayed dead; control outage=%v", outage)
			}
			time.Sleep(20 * time.Millisecond)
		}
		assertTraffic()
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	offline.Store(false)
	// A stalled accounting request must not prevent real process recovery.
	// Counter values are a local fixture; traffic and process lifecycle below
	// use the real engine, without contacting production or changing its routes.
	func() {
		sender := &blockedUsageSender{started: make(chan struct{}), release: make(chan struct{})}
		reporter, err := usageagent.NewReporter(usageagent.NewJournalStore(filepath.Join(t.TempDir(), "usage.json")),
			&finalCounter{}, sender, "loopback-node", "loopback-credential", 10, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		a.usageReporter = reporter
		flushDone := make(chan error, 1)
		go func() { flushDone <- a.flushUsage(context.Background()) }()
		var recoveryDone chan error
		recoveryFinished := false
		defer func() {
			close(sender.release)
			<-flushDone
			if recoveryDone != nil && !recoveryFinished {
				<-recoveryDone
			}
			a.usageReporter = nil
		}()
		select {
		case <-sender.started:
		case <-time.After(time.Second):
			t.Fatal("stalled accounting request never started")
		}
		process, err := os.FindProcess(a.xrayAdapter.RuntimeMetrics().PID)
		if err != nil {
			t.Fatal(err)
		}
		if err := process.Kill(); err != nil {
			t.Fatal(err)
		}
		_ = process.Release()
		deadline := time.Now().Add(3 * time.Second)
		for a.activeEngineMode() != "" {
			if time.Now().After(deadline) {
				t.Fatal("killed engine still reported running")
			}
			time.Sleep(10 * time.Millisecond)
		}
		recoveryDone = make(chan error, 1)
		go func() { recoveryDone <- a.recoverEngine(context.Background()) }()
		select {
		case err := <-recoveryDone:
			recoveryFinished = true
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("slow usage delivery blocked real engine recovery")
		}
		assertTraffic()
	}()
	command.Store(1)
	if err := a.ControlOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	next := desired
	next.Generation = 2
	if err := a.applyWithUsage(context.Background(), next); !errors.Is(err, errEngineStopped) {
		t.Fatalf("new generation bypassed operator stop: %v", err)
	}
	if err := a.recoverEngine(context.Background()); err != nil || a.activeEngineMode() != "" {
		t.Fatalf("operator stop was undone: %v", err)
	}
	_ = a.Close(context.Background())
	restarted := makeAgent()
	trafficAgent = restarted
	if err := restarted.recoverEngine(context.Background()); err != nil || restarted.activeEngineMode() != "" {
		t.Fatalf("stop did not survive agent restart: %v", err)
	}
	command.Store(2)
	if err := restarted.ControlOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertTraffic()
	if err := restarted.applyWithUsage(context.Background(), next); err != nil {
		t.Fatalf("new generation after restart: %v", err)
	}
	assertTraffic()
	_ = restarted.Close(context.Background())
	offline.Store(true)
	local := makeAgent()
	trafficAgent = local
	assertTraffic()
	current, err := local.state.Load()
	if err != nil || current.AppliedGeneration() != 2 || len(current.PendingReceipts) != 2 {
		t.Fatalf("offline bootstrap lost applied configuration or durable receipts: %v", err)
	}
}
