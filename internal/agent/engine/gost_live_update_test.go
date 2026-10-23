package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/forwarding/gostconfig"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestGOSTRealLiveMutationResponseContract(t *testing.T) {
	f := newGOSTUpdateFixture(t)
	initial := f.prepare(gostUpdateRule{id: 1, port: f.ports[0]})
	f.apply(initial)
	raw, err := os.ReadFile(initial.adapterPath)
	if err != nil {
		t.Fatal(err)
	}
	var config struct{ Services []json.RawMessage }
	if err := json.Unmarshal(raw, &config); err != nil || len(config.Services) != 1 {
		t.Fatal("missing isolated service")
	}
	for _, method := range []string{http.MethodDelete, http.MethodPost} {
		path, body := "/config/services/forward-1", ""
		if method == http.MethodPost {
			path, body = "/config/services", string(config.Services[0])
		}
		req, err := http.NewRequestWithContext(f.ctx, method, "http://"+f.a.api.Addr+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.SetBasicAuth(f.a.api.Auth.Username, f.a.api.Auth.Password)
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 3 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]json.RawMessage
		err = json.NewDecoder(resp.Body).Decode(&result)
		_ = resp.Body.Close()
		client.CloseIdleConnections()
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatal("invalid isolated mutation response")
		}
		var code *int
		var msg string
		if len(result["code"]) != 0 && json.Unmarshal(result["code"], &code) != nil || json.Unmarshal(result["msg"], &msg) != nil || code != nil && *code != 0 || code == nil && msg != "OK" {
			t.Fatal("unrecognized isolated mutation acknowledgement")
		}
		t.Logf("%s: recognized engine success acknowledgement", method)
	}
	// Exercise the production parser against the same actual engine responses.
	if _, err := f.r.ExecGOSTRunner.GOSTAPI(f.ctx, f.a.api, http.MethodDelete, "/config/services/forward-1", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.r.ExecGOSTRunner.GOSTAPI(f.ctx, f.a.api, http.MethodPost, "/config/services", config.Services[0]); err != nil {
		t.Fatal(err)
	}
	if err := waitGOSTServiceReady(f.ctx, f.r, f.a.api, "forward-1"); err != nil {
		t.Fatal(err)
	}
	c := f.dial(f.ports[0], 'A')
	_ = c.Close()
}

// Mutate the real engine before reporting an error: compensation must handle
// partial success, not just failures that left runtime untouched.
type gostUpdateFaultRunner struct {
	*ExecGOSTRunner
	fail                             string
	uncertain, failUndo, undoBlocked bool
	after                            func(string, string, []byte)
}

func (r *gostUpdateFaultRunner) GOSTAPI(ctx context.Context, api *gostLocalAPI, method, path string, body []byte) ([]byte, error) {
	if r.undoBlocked && method == "GET" {
		return nil, errors.New("injected compensation read failure")
	}
	raw, err := r.ExecGOSTRunner.GOSTAPI(ctx, api, method, path, body)
	if err == nil && r.after != nil {
		r.after(method, path, body)
	}
	if err == nil && r.fail == method+" "+path {
		r.fail = ""
		r.undoBlocked = r.failUndo
		if r.uncertain {
			return nil, errGOSTAPIUncertain
		}
		return nil, ErrGOSTLiveUpdate
	}
	return raw, err
}

func TestGOSTRealLiveUncertainFailureKeepsExistingStreamAndBlocksChanges(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprintf("uncertain-rpc-%v", uncertain), func(t *testing.T) {
			f := newGOSTUpdateFixture(t)
			changed, stable := gostUpdateRule{id: 1, port: f.ports[0]}, gostUpdateRule{id: 2, port: f.ports[1]}
			initial := f.prepare(changed, stable)
			f.apply(initial)
			defer f.monitor(f.dial(stable.port, 'A'))()
			changed.port, changed.target = f.ports[2], 1
			candidate := f.prepare(changed, stable)
			f.r.fail, f.r.failUndo, f.r.uncertain = "POST /config/services", !uncertain, uncertain
			if err := f.a.Commit(f.ctx, candidate); !errors.Is(err, ErrLiveStateUncertain) {
				t.Fatalf("uncertain failure not surfaced: %v", err)
			}
			if f.r.fail != "" {
				t.Fatal("fault was not exercised")
			}
			f.r.undoBlocked = false
			for i := 0; i < 2; i++ {
				if err := f.a.Commit(f.ctx, candidate); !errors.Is(err, ErrLiveStateUncertain) {
					t.Fatalf("uncertain commit: %v", err)
				}
				if err := f.a.Rollback(f.ctx, &initial); !errors.Is(err, ErrLiveStateUncertain) {
					t.Fatalf("uncertain rollback: %v", err)
				}
				if err := f.a.Verify(f.ctx, initial); !errors.Is(err, ErrLiveStateUncertain) {
					t.Fatalf("uncertain verify: %v", err)
				}
			}
		})
	}
}

func TestGOSTRealLiveUncertainStateRecoversAfterProcessExit(t *testing.T) {
	f := newGOSTUpdateFixture(t)
	changed, stable := gostUpdateRule{id: 1, port: f.ports[0]}, gostUpdateRule{id: 2, port: f.ports[1]}
	initial := f.prepare(changed, stable)
	f.apply(initial)
	original, epoch := f.a.process, f.a.CounterEpoch()
	stream := f.dial(stable.port, 'A')
	changed.port, changed.target = f.ports[2], 1
	candidate := f.prepare(changed, stable)
	f.r.fail, f.r.uncertain = "POST /config/services", true
	if err := f.a.Commit(f.ctx, candidate); !errors.Is(err, ErrLiveStateUncertain) || f.r.fail != "" {
		t.Fatalf("uncertain mutation not exercised: %v", err)
	}
	if err := gostUpdateExchange(stream, 'A'); err != nil {
		t.Fatalf("unrelated stream failed before process exit: %v", err)
	}
	if err := f.a.LiveUpdateError(); !errors.Is(err, ErrLiveStateUncertain) {
		t.Fatalf("live uncertainty guard missing: %v", err)
	}
	if f.a.process != original || f.a.CounterEpoch() != epoch {
		t.Fatal("uncertain mutation replaced the live process")
	}
	// This is an isolated process-exit fault, not a production recovery action.
	// Existing sockets cannot survive the actual core exit; verify new traffic
	// after reactivation rather than claiming continuous service across it.
	if err := original.Stop(f.ctx); err != nil {
		t.Fatal(err)
	}
	if original.Running() {
		t.Fatal("isolated process did not exit")
	}
	if err := f.a.LiveUpdateError(); err != nil {
		t.Fatalf("dead process remained blocked from recovery: %v", err)
	}
	f.apply(initial)
	if f.a.process == original || f.a.CounterEpoch() != epoch+1 {
		t.Fatal("recovery did not establish one new process epoch")
	}
	if err := f.a.LiveUpdateError(); err != nil {
		t.Fatalf("recovered process retained uncertain state: %v", err)
	}
	_ = f.dial(f.ports[0], 'A').Close()
	_ = f.dial(stable.port, 'A').Close()
	f.closed(changed.port)
}

type gostUpdateRule struct {
	id, port, target int
	chain            bool
	chainRevision    int
}
type gostUpdateFixture struct {
	t           *testing.T
	a           *GOSTProcessAdapter
	r           *gostUpdateFaultRunner
	ctx         context.Context
	ports       [4]int
	targets     [2]int
	tunnelPorts [4]int
	generation  agentv1.NodeConfigGeneration
}

func newGOSTUpdateFixture(t *testing.T) *gostUpdateFixture {
	t.Helper()
	binary := os.Getenv("NYVP_GOST_BINARY")
	if binary == "" {
		t.Skip("set NYVP_GOST_BINARY to run real loopback GOST updates")
	}
	r, err := NewExecGOSTRunner(binary)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	f := &gostUpdateFixture{t: t, ctx: ctx, r: &gostUpdateFaultRunner{ExecGOSTRunner: r}}
	port := func() int {
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		return l.Addr().(*net.TCPAddr).Port
	}
	for i := range f.ports {
		f.ports[i] = port()
		l, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		f.tunnelPorts[i] = l.LocalAddr().(*net.UDPAddr).Port
		_ = l.Close()
	}
	for i := range f.targets {
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		f.targets[i] = l.Addr().(*net.TCPAddr).Port
		t.Cleanup(func() { _ = l.Close() })
		marker := byte('A' + i)
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				go func() {
					defer c.Close()
					_ = c.SetDeadline(time.Now().Add(90 * time.Second))
					buffer := make([]byte, 1024)
					for {
						n, err := c.Read(buffer)
						if err != nil {
							return
						}
						for j := 0; j < n; j++ {
							buffer[j] = marker
						}
						if _, err = c.Write(buffer[:n]); err != nil {
							return
						}
					}
				}()
			}
		}()
	}
	root := t.TempDir()
	f.a, err = NewGOSTProcessAdapter(GOSTAdapterOptions{StagingDir: filepath.Join(root, "staging"), ActivePath: filepath.Join(root, "active.json"), Runner: f.r, AutoStart: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.a.Close(context.Background()) })
	return f
}

func (f *gostUpdateFixture) prepare(rules ...gostUpdateRule) PreparedConfiguration {
	f.t.Helper()
	f.generation++
	services, chains := []any{}, []any{}
	for _, r := range rules {
		handler := map[string]any{"type": "tcp"}
		target := fmt.Sprintf("127.0.0.1:%d", f.targets[r.target])
		forwarder := map[string]any{"nodes": []any{map[string]any{"name": "target", "addr": target}}, "selector": map[string]any{"strategy": "round", "maxFails": 1, "failTimeout": 30000000000}}
		if r.chain {
			name := fmt.Sprintf("tunnel-%d", r.id)
			handler["chain"] = name
			addr := fmt.Sprintf("127.0.0.1:%d", f.tunnelPorts[r.id])
			auth := map[string]any{"username": "hl-loopback", "password": strings.Repeat("p", 32)}
			transport := map[string]any{"type": "kcp", "metadata": gostconfig.KCPMetadata(strings.Repeat("k", 32))}
			chains = append(chains, map[string]any{"name": name, "hops": []any{map[string]any{"name": name, "selector": map[string]any{"strategy": "round", "maxFails": 1, "failTimeout": 5000000000}, "nodes": []any{map[string]any{"name": fmt.Sprintf("exit-%d", r.chainRevision), "addr": addr, "connector": map[string]any{"type": "relay", "auth": auth}, "dialer": transport}}}}})
			services = append(services, map[string]any{"name": fmt.Sprintf("tunnel-exit-%d", r.id), "addr": addr, "handler": map[string]any{"type": "relay", "auth": auth, "metadata": map[string]any{"readTimeout": "5s"}}, "listener": transport, "metadata": map[string]any{"enableStats": true}, "forwarder": forwarder})
		}
		services = append(services, map[string]any{"name": fmt.Sprintf("forward-%d", r.id), "addr": fmt.Sprintf("127.0.0.1:%d", r.port), "handler": handler, "listener": map[string]any{"type": "tcp"}, "metadata": map[string]any{"enableStats": true}, "forwarder": forwarder})
	}
	data, err := json.Marshal(map[string]any{"services": services, "chains": chains})
	if err != nil {
		f.t.Fatal(err)
	}
	p, err := f.a.Prepare(f.ctx, testDesired(f.t, f.generation, []agentv1.ConfigurationFragment{gostFragment("independent", string(data))}))
	if err != nil {
		f.t.Fatal(err)
	}
	return p
}

func gostUpdateExchange(c net.Conn, marker byte) error {
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.WriteString(c, "steady-session"); err != nil {
		return err
	}
	b := make([]byte, len("steady-session"))
	if _, err := io.ReadFull(c, b); err != nil {
		return err
	}
	for _, v := range b {
		if v != marker {
			return errors.New("incorrect forwarding target")
		}
	}
	return nil
}
func (f *gostUpdateFixture) dial(port int, marker byte) net.Conn {
	f.t.Helper()
	var c net.Conn
	var err error
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		c, err = net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = c.Close() })
	if err := gostUpdateExchange(c, marker); err != nil {
		f.t.Fatal(err)
	}
	return c
}
func (f *gostUpdateFixture) closed(port int) {
	f.t.Helper()
	c, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
	if err == nil {
		_ = c.Close()
		f.t.Fatal("deleted service still listening")
	}
}
func (f *gostUpdateFixture) monitor(c net.Conn) func() {
	f.t.Helper()
	process, epoch := f.a.process, f.a.CounterEpoch()
	stop, done := make(chan struct{}), make(chan error, 1)
	var samples atomic.Int64
	go func() {
		for {
			select {
			case <-stop:
				done <- nil
				return
			default:
			}
			if err := gostUpdateExchange(c, 'A'); err != nil {
				done <- err
				return
			}
			samples.Add(1)
			time.Sleep(2 * time.Millisecond)
		}
	}()
	return func() {
		close(stop)
		if err := <-done; err != nil {
			f.t.Errorf("unrelated established stream interrupted: %v", err)
		}
		if samples.Load() == 0 || f.a.process != process || f.a.CounterEpoch() != epoch {
			f.t.Error("no traffic samples or GOST restarted")
		}
		f.t.Logf("same TCP session: %d successful exchanges", samples.Load())
	}
}
func (f *gostUpdateFixture) apply(p PreparedConfiguration) {
	f.t.Helper()
	if err := f.a.Commit(f.ctx, p); err != nil {
		f.t.Fatal(err)
	}
	if err := f.a.Verify(f.ctx, p); err != nil {
		f.t.Fatal(err)
	}
}

func TestGOSTRealLiveDeleteDisableEditAndRollbackPreserveStream(t *testing.T) {
	f := newGOSTUpdateFixture(t)
	first, middle, stable := gostUpdateRule{id: 1, port: f.ports[0]}, gostUpdateRule{id: 2, port: f.ports[1]}, gostUpdateRule{id: 3, port: f.ports[2]}
	f.apply(f.prepare(first, middle, stable))
	defer f.monitor(f.dial(stable.port, 'A'))()
	f.apply(f.prepare(middle, stable))
	f.closed(first.port)
	disabled := f.prepare(stable)
	f.apply(disabled)
	f.closed(middle.port)
	resumed := f.prepare(middle, stable)
	f.apply(resumed)
	f.dial(middle.port, 'A')
	edited := middle
	edited.port = f.ports[3]
	edited.target = 1
	f.apply(f.prepare(edited, stable))
	f.closed(middle.port)
	f.dial(edited.port, 'B')
	edited.target = 0
	f.apply(f.prepare(edited, stable))
	f.dial(edited.port, 'A')
	if err := f.a.Rollback(f.ctx, &disabled); err != nil {
		t.Fatal(err)
	}
	f.closed(edited.port)
	if err := f.a.Rollback(f.ctx, &resumed); err != nil {
		t.Fatal(err)
	}
	f.dial(middle.port, 'A')
}

func TestGOSTRealLivePartialFailurePreservesStream(t *testing.T) {
	for _, at := range []string{"DELETE /config/services/forward-1", "POST /config/services", "bind"} {
		t.Run(at, func(t *testing.T) {
			f := newGOSTUpdateFixture(t)
			changed, stable := gostUpdateRule{id: 1, port: f.ports[0]}, gostUpdateRule{id: 2, port: f.ports[1]}
			initial := f.prepare(changed, stable)
			f.apply(initial)
			defer f.monitor(f.dial(stable.port, 'A'))()
			changed.port = f.ports[2]
			changed.target = 1
			changed.chainRevision++
			if at == "bind" {
				l, err := net.Listen("tcp4", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer l.Close()
				changed.port = l.Addr().(*net.TCPAddr).Port
			} else {
				f.r.fail = at
			}
			if err := f.a.Commit(f.ctx, f.prepare(changed, stable)); !errors.Is(err, ErrGOSTLiveUpdate) {
				t.Fatalf("failure not surfaced: %v", err)
			}
			if err := f.a.Verify(f.ctx, initial); err != nil {
				t.Fatal(err)
			}
			f.dial(f.ports[0], 'A')
			if at != "bind" {
				f.closed(changed.port)
			}
			changed.port = f.ports[2]
			f.apply(f.prepare(changed, stable))
			f.dial(changed.port, 'B')
		})
	}
}

func TestGOSTRealLiveEarlyFailurePreservesUnattemptedChangedService(t *testing.T) {
	f := newGOSTUpdateFixture(t)
	first, later, stable := gostUpdateRule{id: 1, port: f.ports[0]}, gostUpdateRule{id: 2, port: f.ports[1]}, gostUpdateRule{id: 3, port: f.ports[2]}
	initial := f.prepare(first, later, stable)
	f.apply(initial)
	defer f.monitor(f.dial(later.port, 'A'))()
	defer f.monitor(f.dial(stable.port, 'A'))()
	first.target, later.target = 1, 1
	candidate := f.prepare(first, later, stable)
	f.r.fail = "DELETE /config/services/forward-1"
	var checkpoints int
	f.r.after = func(method, path string, body []byte) {
		if method == "DELETE" && path == "/config/services/forward-2" || method == "POST" && path == "/config/services" && gostObjectName(body) == "forward-2" {
			t.Fatal("unattempted changed service was rebuilt during compensation")
		}
		for _, port := range []int{later.port, stable.port} {
			c, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
			if err != nil {
				t.Fatal("untouched listener interrupted", err)
			}
			err = gostUpdateExchange(c, 'A')
			_ = c.Close()
			if err != nil {
				t.Fatal("untouched forwarding interrupted", err)
			}
			checkpoints++
		}
	}
	if err := f.a.Commit(f.ctx, candidate); !errors.Is(err, ErrGOSTLiveUpdate) {
		t.Fatalf("error=%v", err)
	}
	if f.r.fail != "" || checkpoints == 0 {
		t.Fatal("fault/checkpoints not exercised")
	}
	if err := f.a.Verify(f.ctx, initial); err != nil {
		t.Fatal(err)
	}
	f.dial(first.port, 'A')
	t.Logf("fresh sessions with complete replies: %d", checkpoints)
}

func TestGOSTRealLiveReliableChainUpdateAndPartialFailure(t *testing.T) {
	for _, at := range []string{"success", "DELETE /config/chains/tunnel-1", "POST /config/chains"} {
		t.Run(at, func(t *testing.T) {
			f := newGOSTUpdateFixture(t)
			if !f.a.SupportsReliableUDP() {
				t.Skip("reliable chain tests require the actual pinned patched Linux GOST binary")
			}
			changed, stable := gostUpdateRule{id: 1, port: f.ports[0], chain: true}, gostUpdateRule{id: 2, port: f.ports[1]}
			initial := f.prepare(changed, stable)
			f.apply(initial)
			f.dial(changed.port, 'A')
			defer f.monitor(f.dial(stable.port, 'A'))()
			changed.chainRevision++
			if at != "success" {
				f.r.fail = at
				if err := f.a.Commit(f.ctx, f.prepare(changed, stable)); !errors.Is(err, ErrGOSTLiveUpdate) {
					t.Fatalf("failure not surfaced: %v", err)
				}
				if err := f.a.Verify(f.ctx, initial); err != nil {
					t.Fatal(err)
				}
				f.dial(changed.port, 'A')
			}
			f.apply(f.prepare(changed, stable))
			f.dial(changed.port, 'A')
			if err := f.a.Rollback(f.ctx, &initial); err != nil {
				t.Fatal(err)
			}
			f.dial(changed.port, 'A')
		})
	}
}

func TestGOSTRealLiveDeleteLastRuleAndResume(t *testing.T) {
	f := newGOSTUpdateFixture(t)
	rule := gostUpdateRule{id: 1, port: f.ports[0]}
	initial := f.prepare(rule)
	f.apply(initial)
	f.dial(rule.port, 'A')
	process, epoch := f.a.process, f.a.CounterEpoch()
	f.apply(f.prepare())
	f.closed(rule.port)
	if err := f.a.Rollback(f.ctx, &initial); err != nil {
		t.Fatal(err)
	}
	f.dial(rule.port, 'A')
	if f.a.process != process || f.a.CounterEpoch() != epoch {
		t.Fatal("last rule removal/resume restarted GOST")
	}
}

func TestGOSTLivePlannerGlobalBoundaryAndChainDependents(t *testing.T) {
	before := []byte(`{"services":[{"name":"a","handler":{"chain":"shared"}},{"name":"b","listener":{"chain":"shared"}},{"name":"steady"}],"chains":[{"name":"shared","hops":[]}],"api":{"addr":"127.0.0.1:9"}}`)
	after := []byte(strings.Replace(string(before), `"hops":[]`, `"hops":[{"name":"new"}]`, 1))
	delta, ok := planGOSTLiveChanges(before, after)
	if !ok || len(delta.services.removed) != 2 || len(delta.services.added) != 2 {
		t.Fatal("chain dependents were not locally recreated")
	}
	for _, raw := range delta.services.removed {
		if gostObjectName(raw) == "steady" {
			t.Fatal("unrelated service changed")
		}
	}
	if _, ok := planGOSTLiveChanges(before, []byte(strings.Replace(string(after), "127.0.0.1:9", "127.0.0.1:10", 1))); ok {
		t.Fatal("global/API changes admitted")
	}
}
