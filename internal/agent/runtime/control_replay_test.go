package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// A lost result must not turn one administrator restart into repeated outages.
// All listeners, control responses and traffic stay on loopback.
func TestRealControlRestartIsNotReplayedWhenResultIsLost(t *testing.T) {
	binary := os.Getenv("NYVP_XRAY_BINARY")
	if binary == "" {
		t.Skip("set NYVP_XRAY_BINARY for real control replay acceptance")
	}
	ports := map[int]bool{}
	reservePort := func() int {
		for {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			p := l.Addr().(*net.TCPAddr).Port
			_ = l.Close()
			if !ports[p] {
				ports[p] = true
				return p
			}
		}
	}
	port := reservePort()
	native := json.RawMessage(fmt.Sprintf(`{"log":{"loglevel":"none"},"inbounds":[{"tag":"replay","listen":"127.0.0.1","port":%d,"protocol":"socks","settings":{"auth":"noauth","udp":false}}],"outbounds":[{"protocol":"freedom"}]}`, port))
	bundle, err := json.Marshal(agentv1.ConfigurationBundle{SchemaVersion: 1, Fragments: []agentv1.ConfigurationFragment{{GroupID: "replay", GroupRevision: 1, Engine: agentv1.EngineXray, Config: native}}})
	if err != nil {
		t.Fatal(err)
	}
	desired := agentv1.DesiredNodeConfig{Generation: 1, Engine: agentv1.EngineNodeBundle, ConfigSHA256: generations.SHA256Hex(bundle), Config: bundle}
	var resultFailure atomic.Bool
	resultFailure.Store(true)
	var reports atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/agent/enroll":
			_ = json.NewEncoder(w).Encode(agentv1.EnrollmentResponse{NodeID: "replay-node", NodeCredential: "loopback-credential"})
		case "/api/v1/agent/desired":
			_ = json.NewEncoder(w).Encode(desired)
		case "/api/v1/agent/control":
			_ = json.NewEncoder(w).Encode(agentv1.ControlCommandEnvelope{ID: "control_100", Command: agentv1.ControlRestart})
		case "/api/v1/agent/control-results":
			var result agentv1.ControlCommandResultRequest
			if err := json.NewDecoder(r.Body).Decode(&result); err != nil || result.ID != "control_100" || result.Status != "succeeded" {
				t.Errorf("result = %+v, error = %v", result, err)
			}
			reports.Add(1)
			if resultFailure.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/agent/heartbeat", "/api/v1/agent/apply-results":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("NYVP_ENROLLMENT_TOKEN", "loopback-token")
	apiAddress := fmt.Sprintf("127.0.0.1:%d", reservePort())
	cfg := config.Config{
		ControlPlaneURL: server.URL, AllowInsecureLoopback: true,
		DataDir: filepath.Join(t.TempDir(), "agent"), Hostname: "loopback",
		AgentVersion: "test", EnrollmentTokenEnv: "NYVP_ENROLLMENT_TOKEN",
		EngineMode: config.EngineModeXray, XrayBinaryPath: binary, XrayAutoStart: true,
		XrayAPIAddress: apiAddress, XrayStatsAPIAddress: apiAddress,
		RequestTimeout: config.Duration(2 * time.Second),
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
	if applied, err := a.DesiredOnce(context.Background()); err != nil || !applied {
		t.Fatalf("initial apply = %v, %v", applied, err)
	}
	initialEpoch := a.xrayAdapter.CounterEpoch()
	if err := a.ControlOnce(context.Background()); err == nil {
		t.Fatal("simulated result loss did not reach the agent")
	}
	if a.xrayAdapter.CounterEpoch() == initialEpoch {
		t.Fatal("the original restart command was not executed")
	}
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	openTraffic := func() net.Conn {
		t.Helper()
		var c net.Conn
		deadline := time.Now().Add(3 * time.Second)
		for {
			c, err = net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal(err)
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Cleanup(func() { _ = c.Close() })
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		_, _ = c.Write([]byte{5, 1, 0})
		var greeting [2]byte
		if _, err := io.ReadFull(c, greeting[:]); err != nil || greeting != [2]byte{5, 0} {
			t.Fatalf("SOCKS greeting = %v, %v", greeting, err)
		}
		p := echo.Addr().(*net.TCPAddr).Port
		_, _ = c.Write([]byte{5, 1, 0, 1, 127, 0, 0, 1, byte(p >> 8), byte(p)})
		var reply [10]byte
		if _, err := io.ReadFull(c, reply[:]); err != nil || reply[1] != 0 {
			t.Fatalf("SOCKS connect = %v, %v", reply, err)
		}
		return c
	}
	exchange := func(c net.Conn) {
		t.Helper()
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		want := []byte("same-customer-connection")
		if _, err := c.Write(want); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, len(want))
		if _, err := io.ReadFull(c, got); err != nil || string(got) != string(want) {
			t.Fatalf("existing customer connection interrupted: %q, %v", got, err)
		}
	}
	for phase := 0; phase < 2; phase++ {
		c := openTraffic()
		exchange(c)
		pid, epoch := a.xrayAdapter.RuntimeMetrics().PID, a.xrayAdapter.CounterEpoch()
		var wg sync.WaitGroup
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := a.ControlOnce(context.Background()); (err != nil) != resultFailure.Load() {
					t.Errorf("retry result error = %v", err)
				}
			}()
		}
		wg.Wait()
		if a.xrayAdapter.RuntimeMetrics().PID != pid || a.xrayAdapter.CounterEpoch() != epoch {
			t.Fatalf("phase %d: one command restarted Xray again after result loss", phase)
		}
		for i := 0; i < 50; i++ {
			exchange(c)
		}
		_ = c.Close()
		if phase == 0 {
			if err := a.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			a = makeAgent() // Reopen the same durable journal after an Agent restart.
			resultFailure.Store(false)
		}
	}
	if got := reports.Load(); got != 9 {
		t.Fatalf("results were not retried: got %d, want 9", got)
	}
	t.Log("same command reported 9 times, executed once; 100 exchanges survived retries on unchanged sockets and engine epochs")
}
