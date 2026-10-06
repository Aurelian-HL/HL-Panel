package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/config"
	"github.com/hongle/hl-panel/internal/agent/controlclient"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestBootstrapRetriesLostEnrollmentResponseWithSamePrivateAttempt(t *testing.T) {
	var calls atomic.Int32
	var original agentv1.EnrollmentRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input agentv1.EnrollmentRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		switch calls.Add(1) {
		case 1:
			original = input
			w.WriteHeader(http.StatusServiceUnavailable)
		case 2:
			if input.EnrollmentSecret != original.EnrollmentSecret {
				t.Error("retry changed recovery secret")
			}
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			connection.Close()
		default:
			if input.EnrollmentSecret != original.EnrollmentSecret {
				t.Error("lost response changed recovery secret")
			}
			json.NewEncoder(w).Encode(agentv1.EnrollmentResponse{NodeID: "recovered-node", NodeCredential: "recovered-credential"})
		}
	}))
	defer server.Close()
	t.Setenv("NYVP_ENROLLMENT_TOKEN", "enr_test_attempt")
	cfg := config.Config{ControlPlaneURL: server.URL, AllowInsecureLoopback: true, DataDir: filepath.Join(t.TempDir(), "state"),
		Hostname: "test-node", AgentVersion: "test", EnrollmentTokenEnv: "NYVP_ENROLLMENT_TOKEN", EngineMode: config.EngineModeDryRun,
		RequestTimeout: config.Duration(time.Second), Backoff: config.BackoffConfig{Initial: config.Duration(time.Millisecond), Maximum: config.Duration(time.Millisecond), Multiplier: 1}}
	a, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.bootstrapWithRetry(ctx); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 || a.nodeID != "recovered-node" {
		t.Fatal("bootstrap did not recover bounded transient failures")
	}
	if len(original.EnrollmentSecret) != 64 {
		t.Fatal("missing private attempt")
	}
	if _, err := a.credential.Load(); err != nil {
		t.Fatal("identity not persisted")
	}
}

func TestBootstrapDoesNotRetryRejectedToken(t *testing.T) {
	for _, status := range []int{400, 401, 403, 409} {
		if retryableBootstrap(&controlclient.HTTPError{StatusCode: status}) {
			t.Fatal("permanent rejection retried")
		}
	}
	if retryableBootstrap(errors.New("invalid private state")) {
		t.Fatal("corrupt local state retried")
	}
}
