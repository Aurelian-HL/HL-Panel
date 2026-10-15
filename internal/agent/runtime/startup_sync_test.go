package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/config"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestRunCompletesInitialHeartbeatBeforeDesiredAndContinuesOnFailure(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		name := "reconcile"
		if unavailable {
			name = "panel_unavailable"
		}
		t.Run(name, func(t *testing.T) {
			var heartbeatCount atomic.Int32
			var initialDone atomic.Bool
			desiredSeen := make(chan struct{})
			var once sync.Once
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/agent/enroll":
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(agentv1.EnrollmentResponse{NodeID: "startup-node", NodeCredential: "startup-credential"})
				case "/api/v1/agent/heartbeat":
					if heartbeatCount.Add(1) == 1 {
						// Expose the old race by holding reconciliation while Run
						// would previously launch the desired loop concurrently.
						time.Sleep(50 * time.Millisecond)
						initialDone.Store(true)
					}
					if unavailable {
						w.WriteHeader(http.StatusServiceUnavailable)
					} else {
						w.WriteHeader(http.StatusNoContent)
					}
				case "/api/v1/agent/desired", "/api/v1/agent/desired/watch":
					if !initialDone.Load() {
						t.Error("desired configuration fetched before initial reconciliation completed")
					}
					once.Do(func() { close(desiredSeen) })
					w.WriteHeader(http.StatusNoContent)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			t.Setenv("NYVP_ENROLLMENT_TOKEN", "startup-enrollment")
			cfg := config.Config{
				ControlPlaneURL: server.URL, AllowInsecureLoopback: true,
				DataDir: filepath.Join(t.TempDir(), "state"), Hostname: "startup-node",
				AgentVersion: "test", EnrollmentTokenEnv: "NYVP_ENROLLMENT_TOKEN",
				EngineMode:        config.EngineModeDryRun,
				HeartbeatInterval: config.Duration(time.Second), DesiredPollInterval: config.Duration(time.Second),
				RequestTimeout: config.Duration(time.Second),
				Backoff:        config.BackoffConfig{Initial: config.Duration(time.Second), Maximum: config.Duration(time.Second), Multiplier: 1},
			}
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
			agent, err := New(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- agent.Run(ctx) }()
			select {
			case <-desiredSeen:
			case <-ctx.Done():
				t.Error("initial heartbeat failure prevented desired configuration loop")
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("unexpected Run result", err)
			}
		})
	}
}
