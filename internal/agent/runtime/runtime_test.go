package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
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
