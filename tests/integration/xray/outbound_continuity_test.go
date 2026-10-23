package xray_test

import (
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/engine"
	"github.com/hongle/hl-panel/internal/agent/reconciler"
	"github.com/hongle/hl-panel/internal/agent/state"
)

func TestLostRollbackJournalDoesNotStopServingXray(t *testing.T) {
	f := newLiveUpdateFixture(t)
	changed, stable := updateRule{id: 1, port: f.ports[0]}, updateRule{id: 2, port: f.ports[1]}
	initial := f.prepare(changed, stable)
	if err := f.a.Commit(f.ctx, initial); err != nil {
		t.Fatal(err)
	}
	defer f.monitor(f.dial(changed.port, 'A'))()
	defer f.monitor(f.dial(stable.port, 'A'))()
	// Only the durable reconciliation journal is missing; the owned process,
	// active native file, API and established sockets are still healthy.
	root := t.TempDir()
	store := state.NewAtomicStateStore(filepath.Join(root, "state.json"), filepath.Join(root, "configurations"))
	r, err := reconciler.New(store, f.a, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	changed.redirect = 2
	candidate := f.prepare(changed, stable)
	var mutations int
	f.runner.after = func(operation string) {
		if operation != "lsi" && operation != "lso" {
			mutations++
		}
	}
	for i := 0; i < 3; i++ {
		if err := r.Apply(f.ctx, candidate.DesiredNodeConfig()); !errors.Is(err, engine.ErrLiveUpdateRequiresMaintenance) {
			t.Fatalf("missing journal delivery not refused: %v", err)
		}
		if err := f.a.Verify(f.ctx, initial); err != nil {
			t.Fatal("serving generation changed after refusal", err)
		}
		for _, port := range []int{changed.port, stable.port} {
			c := f.dial(port, 'A')
			_ = c.Close()
		}
	}
	if mutations != 0 {
		t.Fatalf("refused delivery mutated live API %d times", mutations)
	}
	current, err := store.Load()
	if err != nil || current.Applied != nil || current.Pending != nil {
		t.Fatalf("refused delivery falsely advanced journal: %#v, %v", current, err)
	}
}

// A successful TCP connect alone does not prove forwarding: require the origin's
// complete reply, without retries. Either old or new origin is valid in flight.
func freshUpdateExchange(port int) error {
	c, err := net.DialTimeout("tcp4", localAddress(port), time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	return freshUpdateReply(c)
}

func freshUpdateReply(c net.Conn) error {
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(c, "new-session"); err != nil {
		return err
	}
	reply := make([]byte, len("new-session"))
	if _, err := io.ReadFull(c, reply); err != nil {
		return err
	}
	if reply[0] != 'A' && reply[0] != 'B' {
		return fmt.Errorf("unexpected origin marker %q", reply[0])
	}
	for _, b := range reply {
		if b != reply[0] {
			return fmt.Errorf("mixed origin reply")
		}
	}
	return nil
}

func TestXrayEarlyFailureDoesNotDisturbUntouchedListenersOrRouting(t *testing.T) {
	for _, stage := range []string{"rmi-before", "ado-before", "ado-after", "addition-ado-before", "addition-ado-after"} {
		t.Run(stage, func(t *testing.T) {
			f := newLiveUpdateFixture(t)
			changed, stable := updateRule{id: 1, port: f.ports[0]}, updateRule{id: 2, port: f.ports[1]}
			initial := f.prepare(changed, stable)
			if err := f.a.Commit(f.ctx, initial); err != nil {
				t.Fatal(err)
			}
			defer f.monitor(f.dial(changed.port, 'A'))()
			defer f.monitor(f.dial(stable.port, 'A'))()
			rules := []updateRule{changed, stable}
			if strings.HasPrefix(stage, "addition-") {
				rules = append(rules, updateRule{id: 3, port: f.ports[2]})
			} else {
				rules[0].redirect = 2
				if stage == "rmi-before" {
					rules[0].port = f.ports[2]
				}
			}
			candidate := f.prepare(rules...)
			operation := "ado"
			if stage == "rmi-before" {
				operation = "rmi"
			}
			if strings.HasSuffix(stage, "before") {
				f.runner.reject = operation
			} else {
				f.runner.fail = operation
			}
			f.runner.calls = nil
			var checkpoints int
			f.runner.after = func(_ string) {
				for _, port := range []int{changed.port, stable.port} {
					if err := freshUpdateExchange(port); err != nil {
						t.Fatalf("untouched listener interrupted: %v", err)
					}
					checkpoints++
				}
			}
			if err := f.a.Commit(f.ctx, candidate); !errors.Is(err, engine.ErrXrayLiveUpdate) {
				t.Fatalf("error=%v", err)
			}
			if f.runner.reject != "" || f.runner.fail != "" {
				t.Fatal("fault not exercised")
			}
			counts := map[string]int{}
			for _, call := range f.runner.calls {
				counts[call]++
			}
			wantRMI := 0
			if stage == "rmi-before" {
				wantRMI = 1
			}
			if counts["rmi"] != wantRMI || counts["adrules"] != 0 {
				t.Fatalf("unattempted stages compensated: %v", f.runner.calls)
			}
			if checkpoints == 0 {
				t.Fatal("no fresh forwarding checkpoints")
			}
			if err := f.a.Verify(f.ctx, initial); err != nil {
				t.Fatal(err)
			}
			t.Logf("fresh sessions with complete replies: %d", checkpoints)
		})
	}
}

func TestXrayOutboundEditsKeepExistingAndNewConnections(t *testing.T) {
	for _, failure := range []string{"", "ado", "adrules", "rmo"} {
		name := failure
		if name == "" {
			name = "success"
		}
		t.Run(name, func(t *testing.T) {
			f := newLiveUpdateFixture(t)
			changed := updateRule{id: 1, port: f.ports[0]}
			stable := updateRule{id: 2, port: f.ports[1]}
			initial := f.prepare(changed, stable)
			if err := f.a.Commit(f.ctx, initial); err != nil {
				t.Fatal(err)
			}
			defer f.monitor(f.dial(changed.port, 'A'))()
			defer f.monitor(f.dial(stable.port, 'A'))()

			var checkpoints, fresh atomic.Int64
			// Probe after each completed real mutation, including compensation.
			// This deterministically catches the remove-before-add gap, which a
			// fast machine could otherwise hide from the background dial loop.
			f.runner.after = func(operation string) {
				if operation == "lsi" || operation == "lso" {
					return
				}
				checkpoints.Add(1)
				for _, port := range []int{changed.port, stable.port} {
					if err := freshUpdateExchange(port); err != nil {
						t.Errorf("new forwarding session after %s on %d: %v", operation, port, err)
					}
				}
			}
			stop, done := make(chan struct{}), make(chan error, 1)
			go func() {
				for {
					select {
					case <-stop:
						done <- nil
						return
					default:
					}
					if err := freshUpdateExchange(changed.port); err != nil {
						done <- err
						return
					}
					fresh.Add(1)
					time.Sleep(time.Millisecond)
				}
			}()
			defer func() {
				close(stop)
				if err := <-done; err != nil {
					t.Errorf("concurrent new connection interrupted: %v", err)
				}
				if checkpoints.Load() == 0 || fresh.Load() == 0 {
					t.Error("continuity checks did not exercise live mutations")
				}
				t.Logf("RPC checkpoints=%d, concurrent fresh sessions=%d", checkpoints.Load(), fresh.Load())
			}()
			for i := 0; i < 6; i++ {
				changed.redirect = 1 + i%2
				candidate := f.prepare(changed, stable)
				f.runner.fail = failure
				err := f.a.Commit(f.ctx, candidate)
				if failure != "" {
					if err == nil || f.runner.fail != "" {
						t.Fatalf("failure not exercised: %v", err)
					}
					if err := f.a.Verify(f.ctx, initial); err != nil {
						t.Fatalf("previous generation not restored: %v", err)
					}
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := f.a.Verify(f.ctx, candidate); err != nil {
					t.Fatal(err)
				}
				marker := byte('A' + i%2)
				c := f.dial(changed.port, marker)
				_ = c.Close()
			}
		})
	}
}

// The first delivery to an already running node has old, unversioned tags.
// Exercise migration and its compensation rather than only fresh installs.
func TestXrayLegacyOutboundMigrationPreservesConnections(t *testing.T) {
	for _, failure := range []string{"", "ado", "adrules", "rmo"} {
		name := failure
		if name == "" {
			name = "success"
		}
		t.Run(name, func(t *testing.T) {
			f := newLiveUpdateFixture(t)
			f.legacyTags = true
			changed, stable := updateRule{id: 1, port: f.ports[0]}, updateRule{id: 2, port: f.ports[1]}
			initial := f.prepare(changed, stable)
			if err := f.a.Commit(f.ctx, initial); err != nil {
				t.Fatal(err)
			}
			defer f.monitor(f.dial(changed.port, 'A'))()
			defer f.monitor(f.dial(stable.port, 'A'))()
			var checkpoints int
			f.runner.after = func(operation string) {
				if operation == "lsi" || operation == "lso" {
					return
				}
				checkpoints++
				for _, port := range []int{changed.port, stable.port} {
					if err := freshUpdateExchange(port); err != nil {
						t.Errorf("migration new session after %s: %v", operation, err)
					}
				}
			}
			f.legacyTags = false
			changed.redirect = 2
			candidate := f.prepare(changed, stable)
			f.runner.fail = failure
			err := f.a.Commit(f.ctx, candidate)
			if failure != "" {
				if err == nil || f.runner.fail != "" {
					t.Fatalf("migration fault not exercised: %v", err)
				}
				if err := f.a.Verify(f.ctx, initial); err != nil {
					t.Fatal("legacy generation not restored", err)
				}
				f.dial(changed.port, 'A')
				// The exact same staged delivery can be retried after recovery.
				if err := f.a.Commit(f.ctx, candidate); err != nil {
					t.Fatal("migration retry", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if err := f.a.Verify(f.ctx, candidate); err != nil {
				t.Fatal(err)
			}
			f.dial(changed.port, 'B')
			f.dial(stable.port, 'A')
			if checkpoints == 0 {
				t.Fatal("migration did not exercise live RPCs")
			}
			t.Logf("legacy migration RPC checkpoints=%d", checkpoints)
		})
	}
}
