package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/config"
	"github.com/hongle/hl-panel/internal/agent/state"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func newControlSafetyAgent(t *testing.T, control http.HandlerFunc) *Agent {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/agent/enroll" {
			_ = json.NewEncoder(w).Encode(agentv1.EnrollmentResponse{NodeID: "test-node", NodeCredential: "test-credential"})
			return
		}
		control(w, r)
	}))
	t.Cleanup(server.Close)
	t.Setenv("NYVP_ENROLLMENT_TOKEN", "test-token")
	a, err := New(config.Config{
		ControlPlaneURL: server.URL, AllowInsecureLoopback: true, DataDir: filepath.Join(t.TempDir(), "agent"),
		Hostname: "loopback", AgentVersion: "test", EnrollmentTokenEnv: "NYVP_ENROLLMENT_TOKEN",
		EngineMode: config.EngineModeDryRun, RequestTimeout: config.Duration(5 * time.Second),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(context.Background()) })
	if err := a.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestInvalidControlJournalCannotStopEngineAndStillAllowsStatus(t *testing.T) {
	var statusOnly atomic.Bool
	results := make(chan agentv1.ControlCommandResultRequest, 2)
	a := newControlSafetyAgent(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/agent/control":
			command := agentv1.ControlCommandEnvelope{ID: "stop-1", Command: agentv1.ControlStop}
			if statusOnly.Load() {
				command.ID, command.Command = "status-2", agentv1.ControlStatus
			}
			_ = json.NewEncoder(w).Encode(command)
		case "/api/v1/agent/control-results":
			var result agentv1.ControlCommandResultRequest
			if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
				t.Error(err)
			}
			results <- result
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	if err := os.WriteFile(a.cfg.StatePath()+".control-commands", []byte(`{"version":1`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.ControlOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if result := <-results; result.ID != "stop-1" || result.Status != "failed" {
		t.Fatalf("unsafe control result = %+v", result)
	}
	if stopped, err := a.state.EngineStopped(); err != nil || stopped {
		t.Fatalf("invalid journal did not prevent stop: %v, %v", stopped, err)
	}
	statusOnly.Store(true)
	if err := a.ControlOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if result := <-results; result.ID != "status-2" || result.Status != "succeeded" {
		t.Fatalf("invalid journal blocked read-only status: %+v", result)
	}
}

func TestSlowControlResultDoesNotHoldExecutionOrLifecycleLocks(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var fetches atomic.Int32
	a := newControlSafetyAgent(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/agent/control":
			command := agentv1.ControlCommandEnvelope{ID: "status-1", Command: agentv1.ControlStatus}
			if fetches.Add(1) > 1 {
				command.ID, command.Command = "stop-2", agentv1.ControlStop
			}
			_ = json.NewEncoder(w).Encode(command)
		case "/api/v1/agent/control-results":
			var result agentv1.ControlCommandResultRequest
			if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
				t.Error(err)
			}
			if result.ID == "status-1" {
				close(started)
				select {
				case <-release:
				case <-r.Context().Done():
				}
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	first := make(chan error, 1)
	go func() { first <- a.ControlOnce(context.Background()) }()
	defer func() {
		unblock()
		select {
		case err := <-first:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(6 * time.Second):
			t.Error("first result delivery did not exit")
		}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("slow result delivery never started")
	}
	second := make(chan error, 1)
	go func() { second <- a.ControlOnce(context.Background()) }()
	select {
	case err := <-second:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("slow control result prevented a newer administrator command")
	}
	if stopped, err := a.state.EngineStopped(); err != nil || !stopped {
		t.Fatalf("newer command was not executed during slow delivery: %v, %v", stopped, err)
	}
}

func TestCanceledLifecycleCommandCannotChangeStopIntentAfterWaiting(t *testing.T) {
	for _, restart := range []bool{false, true} {
		name := "stop"
		if restart {
			name = "restart"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			store := state.NewAtomicStateStore(filepath.Join(dir, "state.json"), filepath.Join(dir, "configs"))
			if err := store.SetEngineStopped(restart); err != nil {
				t.Fatal(err)
			}
			a := &Agent{state: store}
			a.usageApplyMu.Lock()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				if restart {
					done <- a.restartEngine(ctx)
				} else {
					done <- a.stopEngine(ctx)
				}
			}()
			cancel()
			a.usageApplyMu.Unlock()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled lifecycle command = %v", err)
			}
			if stopped, err := store.EngineStopped(); err != nil || stopped != restart {
				t.Fatalf("canceled command changed stop intent: %v, %v", stopped, err)
			}
		})
	}
}
