package xray_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/engine"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// Faults are injected after a real RPC, exercising partial success and actual
// handler compensation rather than pretending the failed command did nothing.
type updateFaultRunner struct {
	*engine.ExecXrayRunner
	fail                  string
	failUndo, undoBlocked bool
}

func (r *updateFaultRunner) API(ctx context.Context, address, operation string, doc []byte) ([]byte, error) {
	if r.undoBlocked && (operation == "lsi" || operation == "lso") {
		return nil, errors.New("injected compensation read failure")
	}
	raw, err := r.ExecXrayRunner.API(ctx, address, operation, doc)
	if err == nil && r.fail == operation {
		r.fail = ""
		r.undoBlocked = r.failUndo
		return nil, errors.New("injected confirmed failure after RPC")
	}
	return raw, err
}

func TestXrayLiveCompensationFailureKeepsExistingStreamAndBlocksChanges(t *testing.T) {
	for _, operation := range []string{"rmi", "adrules"} {
		t.Run(operation, func(t *testing.T) {
			f := newLiveUpdateFixture(t)
			changed, stable := updateRule{id: 1, port: f.ports[0]}, updateRule{id: 2, port: f.ports[1]}
			initial := f.prepare(changed, stable)
			if err := f.a.Commit(f.ctx, initial); err != nil {
				t.Fatal(err)
			}
			defer f.monitor(f.dial(stable.port, 'A'))()
			changed.port, changed.target = f.ports[2], 1
			changed.route = stable.id
			candidate := f.prepare(changed, stable)
			f.runner.fail, f.runner.failUndo = operation, true
			if err := f.a.Commit(f.ctx, candidate); !errors.Is(err, engine.ErrLiveStateUncertain) {
				t.Fatalf("compensation failure lost uncertainty: %v", err)
			}
			if f.runner.fail != "" || !f.runner.undoBlocked {
				t.Fatal("fault was not exercised")
			}
			// Even after the API becomes reachable, a later delivery must not
			// pretend that the old file proves the runtime transaction succeeded.
			f.runner.undoBlocked = false
			for i := 0; i < 2; i++ {
				if err := f.a.Commit(f.ctx, candidate); !errors.Is(err, engine.ErrLiveStateUncertain) {
					t.Fatalf("uncertain commit: %v", err)
				}
				if err := f.a.Rollback(f.ctx, &initial); !errors.Is(err, engine.ErrLiveStateUncertain) {
					t.Fatalf("uncertain rollback: %v", err)
				}
				if err := f.a.Verify(f.ctx, initial); !errors.Is(err, engine.ErrLiveStateUncertain) {
					t.Fatalf("uncertain verify: %v", err)
				}
			}
		})
	}
}

type updateRule struct{ id, port, target, redirect, route int }
type liveUpdateFixture struct {
	t          *testing.T
	ctx        context.Context
	a          *engine.XrayProcessAdapter
	runner     *updateFaultRunner
	ports      [4]int
	targets    [2]int
	generation agentv1.NodeConfigGeneration
}

func newLiveUpdateFixture(t *testing.T) *liveUpdateFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	f := &liveUpdateFixture{t: t, ctx: ctx}
	for i := range f.targets {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		f.targets[i] = listener.Addr().(*net.TCPAddr).Port
		t.Cleanup(func() { _ = listener.Close() })
		marker := byte('A' + i)
		go func() {
			for {
				c, err := listener.Accept()
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
	for i := range f.ports {
		f.ports[i] = freeLoopbackPort(t)
	}
	r, err := engine.NewExecXrayRunner(xrayBinary(t))
	if err != nil {
		t.Fatal(err)
	}
	f.runner = &updateFaultRunner{ExecXrayRunner: r}
	root := t.TempDir()
	f.a, err = engine.NewXrayProcessAdapter(engine.XrayAdapterOptions{
		StagingDir: filepath.Join(root, "staging"), ActivePath: filepath.Join(root, "active.json"),
		Runner: f.runner, AutoStart: true, StatsAPIAddress: localAddress(freeLoopbackPort(t)),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.a.Close(context.Background()) })
	return f
}

func (f *liveUpdateFixture) prepare(rules ...updateRule) engine.PreparedConfiguration {
	f.t.Helper()
	f.generation++
	in, out, routes := []any{}, []any{}, []any{}
	for _, r := range rules {
		inTag, outTag := fmt.Sprintf("in-%d", r.id), fmt.Sprintf("out-%d", r.id)
		in = append(in, map[string]any{"tag": inTag, "listen": "127.0.0.1", "port": r.port, "protocol": "dokodemo-door", "settings": map[string]any{"address": "127.0.0.1", "port": f.targets[r.target], "network": "tcp"}})
		settings := map[string]any{}
		if r.redirect > 0 {
			settings["redirect"] = localAddress(f.targets[r.redirect-1])
		}
		out = append(out, map[string]any{"tag": outTag, "protocol": "freedom", "settings": settings})
		route := r.id
		if r.route > 0 {
			route = r.route
		}
		routes = append(routes, map[string]any{"type": "field", "inboundTag": []string{inTag}, "outboundTag": fmt.Sprintf("out-%d", route)})
	}
	data, _ := json.Marshal(map[string]any{"log": map[string]any{"loglevel": "none"}, "inbounds": in, "outbounds": out, "routing": map[string]any{"rules": routes}})
	p, err := f.a.Prepare(f.ctx, adapterDesired(f.t, f.generation, []agentv1.ConfigurationFragment{{GroupID: "independent", GroupRevision: 1, Engine: agentv1.EngineXray, Config: data}}))
	if err != nil {
		f.t.Fatal(err)
	}
	return p
}

func exchangeUpdate(c net.Conn, marker byte) error {
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.WriteString(c, "steady-session"); err != nil {
		return err
	}
	buffer := make([]byte, len("steady-session"))
	if _, err := io.ReadFull(c, buffer); err != nil {
		return err
	}
	for _, b := range buffer {
		if b != marker {
			return errors.New("wrong outbound target")
		}
	}
	return nil
}

func (f *liveUpdateFixture) dial(port int, marker byte) net.Conn {
	f.t.Helper()
	var c net.Conn
	var err error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err = net.DialTimeout("tcp4", localAddress(port), 100*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = c.Close() })
	if err = exchangeUpdate(c, marker); err != nil {
		f.t.Fatal(err)
	}
	return c
}

func (f *liveUpdateFixture) closed(port int) {
	f.t.Helper()
	c, err := net.DialTimeout("tcp4", localAddress(port), 100*time.Millisecond)
	if err == nil {
		_ = c.Close()
		f.t.Fatal("deleted listener still accepts connections")
	}
}

// Continuously use the SAME socket while other rules change. Checking just PID
// or making fresh dials would miss transient interruption of customer sessions.
func (f *liveUpdateFixture) monitor(c net.Conn) func() {
	f.t.Helper()
	pid, epoch := f.a.RuntimeMetrics().PID, f.a.CounterEpoch()
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
			if err := exchangeUpdate(c, 'A'); err != nil {
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
			f.t.Errorf("unrelated established TCP session interrupted: %v", err)
		}
		if samples.Load() == 0 {
			f.t.Error("no stream samples")
		}
		if f.a.RuntimeMetrics().PID != pid || f.a.CounterEpoch() != epoch {
			f.t.Error("rule operation restarted Xray")
		}
		f.t.Logf("same TCP session: %d successful exchanges", samples.Load())
	}
}

func TestXrayLiveDeleteDisableEditAndRollbackPreserveStream(t *testing.T) {
	f := newLiveUpdateFixture(t)
	first, middle, stable := updateRule{id: 1, port: f.ports[0]}, updateRule{id: 2, port: f.ports[1]}, updateRule{id: 3, port: f.ports[2]}
	apply := func(p engine.PreparedConfiguration) {
		t.Helper()
		if err := f.a.Commit(f.ctx, p); err != nil {
			t.Fatal(err)
		}
		if err := f.a.Verify(f.ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	apply(f.prepare(first, middle, stable))
	defer f.monitor(f.dial(stable.port, 'A'))()
	// Delete the first outbound/listener, then disable another, then resume.
	apply(f.prepare(middle, stable))
	f.closed(first.port)
	disabled := f.prepare(stable)
	apply(disabled)
	f.closed(middle.port)
	resumed := f.prepare(middle, stable)
	apply(resumed)
	f.dial(middle.port, 'A')
	// Replace only the edited inbound and verify its new port and destination.
	edited := middle
	edited.port = f.ports[3]
	edited.target = 1
	apply(f.prepare(edited, stable))
	f.closed(middle.port)
	f.dial(edited.port, 'B')
	// Replace an outbound with the same tag, then change only routing.
	edited.redirect = 1
	apply(f.prepare(edited, stable))
	f.dial(edited.port, 'A')
	edited.redirect = 0
	apply(f.prepare(edited, stable))
	f.dial(edited.port, 'B')
	edited.route = 3
	apply(f.prepare(edited, stable))
	f.dial(edited.port, 'B')
	if err := f.a.Rollback(f.ctx, &disabled); err != nil {
		t.Fatal(err)
	}
	if err := f.a.Verify(f.ctx, disabled); err != nil {
		t.Fatal(err)
	}
	f.closed(edited.port)
	// Restoring a prior deletion is another live transaction, not a restart.
	if err := f.a.Rollback(f.ctx, &resumed); err != nil {
		t.Fatal(err)
	}
	f.dial(middle.port, 'A')
}

func TestXrayLiveDeleteLastRuleAndResume(t *testing.T) {
	f := newLiveUpdateFixture(t)
	rule := updateRule{id: 1, port: f.ports[0]}
	initial := f.prepare(rule)
	if err := f.a.Commit(f.ctx, initial); err != nil {
		t.Fatal(err)
	}
	f.dial(rule.port, 'A')
	pid, epoch := f.a.RuntimeMetrics().PID, f.a.CounterEpoch()
	empty := f.prepare()
	if err := f.a.Commit(f.ctx, empty); err != nil {
		t.Fatal(err)
	}
	f.closed(rule.port)
	if err := f.a.Rollback(f.ctx, &initial); err != nil {
		t.Fatal(err)
	}
	f.dial(rule.port, 'A')
	if f.a.RuntimeMetrics().PID != pid || f.a.CounterEpoch() != epoch {
		t.Fatal("last-rule removal or resume restarted Xray")
	}
}

func TestXrayLiveEditPartialFailureRestoresHandlersAndStream(t *testing.T) {
	for _, operation := range []string{"rmi", "rmo", "ado", "adrules", "adi", "bind"} {
		t.Run(operation, func(t *testing.T) {
			f := newLiveUpdateFixture(t)
			changed, stable := updateRule{id: 1, port: f.ports[0]}, updateRule{id: 2, port: f.ports[1]}
			initial := f.prepare(changed, stable)
			if err := f.a.Commit(f.ctx, initial); err != nil {
				t.Fatal(err)
			}
			defer f.monitor(f.dial(stable.port, 'A'))()
			changed.port = f.ports[2]
			changed.target = 1
			changed.redirect = 2
			changed.route = 2
			if operation == "bind" {
				blocked, err := net.Listen("tcp4", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer blocked.Close()
				changed.port = blocked.Addr().(*net.TCPAddr).Port
			} else {
				f.runner.fail = operation
			}
			if err := f.a.Commit(f.ctx, f.prepare(changed, stable)); !errors.Is(err, engine.ErrXrayLiveUpdate) {
				t.Fatalf("failure was not surfaced: %v", err)
			}
			if err := f.a.Verify(f.ctx, initial); err != nil {
				t.Fatalf("LKG not restored: %v", err)
			}
			f.dial(f.ports[0], 'A')
			if operation != "bind" {
				f.closed(changed.port)
			}
			// Reusing the same tag/port after compensation must remain possible.
			changed.port = f.ports[2]
			if err := f.a.Commit(f.ctx, f.prepare(changed, stable)); err != nil {
				t.Fatal(err)
			}
			f.dial(changed.port, 'B')
		})
	}
}
