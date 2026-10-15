package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/config"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestAgentBootstrapHeartbeatAndDesiredApplyOverHTTPTestServer(t *testing.T) {
	var mu sync.Mutex
	var heartbeat agentv1.HeartbeatRequest
	var authorization []string
	var phases []agentv1.ApplyPhase
	desiredConfig := []byte(`{"schema_version":1,"fragments":[]}`)
	desiredHash := sha256.Sum256(desiredConfig)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if request.Header.Get("Authorization") != "" {
			authorization = append(authorization, request.Header.Get("Authorization"))
		}
		switch request.URL.Path {
		case "/api/v1/agent/enroll":
			var enrollment agentv1.EnrollmentRequest
			if err := json.NewDecoder(request.Body).Decode(&enrollment); err != nil {
				t.Errorf("decode enrollment request: %v", err)
			}
			if enrollment.EnrollmentToken != "test-enrollment-token" {
				t.Errorf("enrollment token = %q", enrollment.EnrollmentToken)
			}
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(agentv1.EnrollmentResponse{NodeID: "node-test", NodeCredential: "credential-test"})
		case "/api/v1/agent/heartbeat":
			if err := json.NewDecoder(request.Body).Decode(&heartbeat); err != nil {
				t.Errorf("decode heartbeat request: %v", err)
			}
			writer.WriteHeader(http.StatusNoContent)
		case "/api/v1/agent/desired":
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(agentv1.DesiredNodeConfig{Generation: 1, Engine: agentv1.EngineNodeBundle, ConfigSHA256: hex.EncodeToString(desiredHash[:]), Config: desiredConfig})
		case "/api/v1/agent/apply-results":
			var result agentv1.ApplyResultRequest
			if err := json.NewDecoder(request.Body).Decode(&result); err != nil {
				t.Errorf("decode apply result: %v", err)
			}
			phases = append(phases, result.Phase)
			writer.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	t.Setenv("NYVP_ENROLLMENT_TOKEN", "test-enrollment-token")
	cfg := config.Config{
		ControlPlaneURL:       server.URL,
		AllowInsecureLoopback: true,
		DataDir:               filepath.Join(t.TempDir(), "state"),
		Hostname:              "test-edge",
		AgentVersion:          "test",
		EnrollmentTokenEnv:    "NYVP_ENROLLMENT_TOKEN",
		Capabilities:          []string{"integration-test"},
		HeartbeatInterval:     config.Duration(time.Second),
		DesiredPollInterval:   config.Duration(time.Second),
		RequestTimeout:        config.Duration(2 * time.Second),
		Backoff:               config.BackoffConfig{Initial: config.Duration(time.Second), Maximum: config.Duration(time.Second), Multiplier: 1},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Config.Validate() error = %v", err)
	}
	agent, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := agent.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	if err := agent.HeartbeatOnce(context.Background()); err != nil {
		t.Fatalf("HeartbeatOnce() error = %v", err)
	}
	if applied, err := agent.DesiredOnce(context.Background()); err != nil || !applied {
		t.Fatalf("DesiredOnce() = (%v, %v), want applied", applied, err)
	}

	credentialsData, err := os.ReadFile(cfg.CredentialPath())
	if err != nil {
		t.Fatalf("ReadFile(credentials) error = %v", err)
	}
	if string(credentialsData) == "" || string(credentialsData) == "test-enrollment-token" {
		t.Fatalf("credential file contains unexpected enrollment token content")
	}
	current, err := agent.state.Load()
	if err != nil {
		t.Fatalf("Load(state) error = %v", err)
	}
	if current.Applied == nil || current.Applied.Generation != 1 {
		t.Fatalf("state = %#v, want applied generation 1", current)
	}
	mu.Lock()
	defer mu.Unlock()
	if heartbeat.AppliedGeneration != 0 {
		t.Fatalf("heartbeat applied generation = %d, want 0 before desired apply", heartbeat.AppliedGeneration)
	}
	if len(authorization) < 2 {
		t.Fatalf("authorization headers = %#v, want authenticated calls", authorization)
	}
	if len(phases) != 4 || phases[0] != agentv1.ApplyPhasePrepare || phases[1] != agentv1.ApplyPhaseValidate || phases[2] != agentv1.ApplyPhaseCommit || phases[3] != agentv1.ApplyPhaseVerify {
		t.Fatalf("apply phases = %#v, want prepare/validate/commit/verify", phases)
	}
}

func TestHeartbeatBackoffIsBoundedIndependently(t *testing.T) {
	cfg := config.Config{Backoff: config.BackoffConfig{
		Initial:        config.Duration(2 * time.Second),
		Maximum:        config.Duration(2 * time.Minute),
		Multiplier:     2,
		JitterFraction: 0,
	}}
	settings := heartbeatBackoffConfig(cfg)
	if settings.Maximum.Duration() != heartbeatRetryMaximum {
		t.Fatalf("heartbeat maximum = %s, want %s", settings.Maximum.Duration(), heartbeatRetryMaximum)
	}
	if settings.Initial.Duration() != 2*time.Second {
		t.Fatalf("heartbeat initial = %s, want 2s", settings.Initial.Duration())
	}
	short := config.Config{Backoff: config.BackoffConfig{
		Initial:    config.Duration(30 * time.Second),
		Maximum:    config.Duration(time.Minute),
		Multiplier: 2,
	}}
	shortSettings := heartbeatBackoffConfig(short)
	if shortSettings.Initial.Duration() != heartbeatRetryMaximum || shortSettings.Maximum.Duration() != heartbeatRetryMaximum {
		t.Fatalf("heartbeat initial/maximum = (%s, %s), want both %s", shortSettings.Initial.Duration(), shortSettings.Maximum.Duration(), heartbeatRetryMaximum)
	}
}

func TestAgentRunContinuesHeartbeatWhenProbeEchoPortIsUnavailable(t *testing.T) {
	var heartbeats atomic.Int32
	heartbeatSeen := make(chan struct{})
	var heartbeatOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/agent/enroll":
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(agentv1.EnrollmentResponse{NodeID: "node-run", NodeCredential: "credential-run"})
		case "/api/v1/agent/heartbeat":
			heartbeats.Add(1)
			heartbeatOnce.Do(func() { close(heartbeatSeen) })
			writer.WriteHeader(http.StatusNoContent)
		case "/api/v1/agent/desired":
			writer.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve probe echo port: %v", err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port

	t.Setenv("NYVP_ENROLLMENT_TOKEN", "test-run-token")
	cfg := config.Config{
		ControlPlaneURL:       server.URL,
		AllowInsecureLoopback: true,
		DataDir:               filepath.Join(t.TempDir(), "state"),
		Hostname:              "test-run-node",
		AgentVersion:          "test",
		EnrollmentTokenEnv:    "NYVP_ENROLLMENT_TOKEN",
		EngineMode:            config.EngineModeDryRun,
		ProtocolProbeEchoPort: port,
		HeartbeatInterval:     config.Duration(time.Second),
		DesiredPollInterval:   config.Duration(time.Second),
		RequestTimeout:        config.Duration(time.Second),
		Backoff:               config.BackoffConfig{Initial: config.Duration(5 * time.Millisecond), Maximum: config.Duration(5 * time.Millisecond), Multiplier: 1},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Config.Validate() error = %v", err)
	}
	agent, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	runResult := make(chan error, 1)
	go func() { runResult <- agent.Run(ctx) }()

	select {
	case <-heartbeatSeen:
	case <-ctx.Done():
		t.Fatalf("agent did not send a heartbeat while probe echo port was occupied")
	}
	cancel()
	if err := <-runResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context cancellation", err)
	}
	if heartbeats.Load() < 1 {
		t.Fatal("agent stopped before recording a heartbeat")
	}
}

func TestProbeEchoRecoversAfterInitialPortConflict(t *testing.T) {
	previousInterval := probeEchoRetryInterval
	probeEchoRetryInterval = 10 * time.Millisecond
	t.Cleanup(func() { probeEchoRetryInterval = previousInterval })

	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve probe echo port: %v", err)
	}
	port := occupied.Addr().(*net.TCPAddr).Port
	agent := &Agent{
		cfg:    config.Config{ProtocolProbeEchoPort: port},
		logger: slog.Default(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- agent.probeEchoLoop(ctx) }()

	// The first bind must fail while the port is held, then recover without
	// restarting the Agent after the competing listener is released.
	time.Sleep(20 * time.Millisecond)
	if err := occupied.Close(); err != nil {
		t.Fatalf("release occupied port: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		connection, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 50*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			cancel()
			if runErr := <-done; !errors.Is(runErr, context.Canceled) {
				t.Fatalf("probeEchoLoop() error = %v, want context cancellation", runErr)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	t.Fatalf("probe echo did not recover after the occupied port was released")
}

func TestAgentCloseSerializesWithEngineLifecycle(t *testing.T) {
	agent := &Agent{}
	agent.usageApplyMu.Lock()
	closed := make(chan struct{})
	go func() {
		_ = agent.Close(context.Background())
		close(closed)
	}()

	select {
	case <-closed:
		t.Fatal("Close bypassed the engine lifecycle lock")
	case <-time.After(25 * time.Millisecond):
	}

	agent.usageApplyMu.Unlock()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close remained blocked after the lifecycle lock was released")
	}
}
