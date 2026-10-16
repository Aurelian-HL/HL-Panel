package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/config"
	"github.com/hongle/hl-panel/internal/agent/engine"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type failOnceAdapter struct {
	engine.EngineAdapter
	failed atomic.Bool
}

func (a *failOnceAdapter) Validate(ctx context.Context, prepared engine.PreparedConfiguration) error {
	if a.failed.CompareAndSwap(false, true) {
		return errors.New("simulated transient engine validation failure")
	}
	return a.EngineAdapter.Validate(ctx, prepared)
}

func TestDesiredLoopRetriesSameGenerationAfterTransientApplyFailure(t *testing.T) {
	raw := []byte(`{"schema_version":1,"fragments":[]}`)
	hash := sha256.Sum256(raw)
	desired := agentv1.DesiredNodeConfig{
		Generation: 1, Engine: agentv1.EngineNodeBundle,
		ConfigSHA256: hex.EncodeToString(hash[:]), Config: raw,
	}
	var desiredCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/agent/enroll":
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(agentv1.EnrollmentResponse{NodeID: "retry-node", NodeCredential: "retry-credential"})
		case "/api/v1/agent/desired-watch":
			desiredCalls.Add(1)
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(desired)
		case "/api/v1/agent/desired":
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(desired)
		case "/api/v1/agent/apply-results":
			writer.WriteHeader(http.StatusNoContent)
		default:
			writer.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()

	cfg := config.Config{
		ControlPlaneURL: server.URL, AllowInsecureLoopback: true,
		DataDir: filepath.Join(t.TempDir(), "state"), Hostname: "retry-node",
		AgentVersion: "test", EnrollmentTokenEnv: "NYVP_RETRY_TOKEN",
		EngineMode:          config.EngineModeDryRun,
		HeartbeatInterval:   config.Duration(time.Second),
		DesiredPollInterval: config.Duration(time.Second),
		RequestTimeout:      config.Duration(time.Second),
		Backoff:             config.BackoffConfig{Initial: config.Duration(time.Second), Maximum: config.Duration(time.Second), Multiplier: 1},
	}
	t.Setenv("NYVP_RETRY_TOKEN", "retry-token")
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	agent, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	dry, ok := agent.adapter.(*engine.DryRunFileAdapter)
	if !ok {
		t.Fatalf("adapter type = %T, want dry-run adapter", agent.adapter)
	}
	agent.adapter = &failOnceAdapter{EngineAdapter: dry}
	if err := agent.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- agent.desiredLoop(ctx) }()

	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		current, loadErr := agent.state.Load()
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if current.Applied != nil && current.Applied.Generation == desired.Generation {
			cancel()
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("same desired generation was not retried after transient apply failure")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("desiredLoop() error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("desiredLoop did not stop after cancellation")
	}
	if calls := desiredCalls.Load(); calls < 3 {
		t.Fatalf("desired-watch calls = %d, want a failed apply, wait, and retry", calls)
	}
}

func TestDesiredLoopThrottlesLegacyControlPlanePolling(t *testing.T) {
	raw := []byte(`{"schema_version":1,"fragments":[]}`)
	hash := sha256.Sum256(raw)
	desired := agentv1.DesiredNodeConfig{
		Generation: 1, Engine: agentv1.EngineNodeBundle,
		ConfigSHA256: hex.EncodeToString(hash[:]), Config: raw,
	}
	const pollInterval = time.Second
	var desiredCalls atomic.Int32
	firstDesiredAt := make(chan time.Time, 1)
	secondDesiredAfter := make(chan time.Duration, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/agent/enroll":
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(agentv1.EnrollmentResponse{NodeID: "legacy-node", NodeCredential: "legacy-credential"})
		case "/api/v1/agent/desired-watch":
			http.NotFound(writer, request)
		case "/api/v1/agent/desired":
			call := desiredCalls.Add(1)
			now := time.Now()
			if call == 1 {
				firstDesiredAt <- now
			} else if call == 2 {
				first := <-firstDesiredAt
				secondDesiredAfter <- now.Sub(first)
			}
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(desired)
		case "/api/v1/agent/apply-results":
			writer.WriteHeader(http.StatusNoContent)
		default:
			writer.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()

	cfg := config.Config{
		ControlPlaneURL: server.URL, AllowInsecureLoopback: true,
		DataDir: filepath.Join(t.TempDir(), "state"), Hostname: "legacy-node",
		AgentVersion: "test", EnrollmentTokenEnv: "NYVP_LEGACY_TOKEN",
		EngineMode:          config.EngineModeDryRun,
		HeartbeatInterval:   config.Duration(time.Second),
		DesiredPollInterval: config.Duration(pollInterval),
		RequestTimeout:      config.Duration(time.Second),
		Backoff:             config.BackoffConfig{Initial: config.Duration(time.Second), Maximum: config.Duration(time.Second), Multiplier: 1},
	}
	t.Setenv("NYVP_LEGACY_TOKEN", "legacy-token")
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	agent, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- agent.desiredLoop(ctx) }()

	select {
	case elapsed := <-secondDesiredAfter:
		if elapsed < pollInterval*3/4 {
			t.Fatalf("legacy desired poll interval = %v, want at least %v", elapsed, pollInterval*3/4)
		}
		cancel()
	case <-ctx.Done():
		t.Fatal("legacy panel was not polled a second time")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("desiredLoop() error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("desiredLoop did not stop after cancellation")
	}
}
