package xray_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/engine"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// Omitting the optional API capability reproduces the former full-restart path.
type noLiveXrayRunner struct{ runner *engine.ExecXrayRunner }

func (r noLiveXrayRunner) Test(ctx context.Context, path string) error {
	return r.runner.Test(ctx, path)
}
func (r noLiveXrayRunner) Start(ctx context.Context, path string) (engine.XrayProcess, error) {
	return r.runner.Start(ctx, path)
}

func TestXrayLiveAdditionPreservesActualTCPStream(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(fmt.Sprintf("live=%v", live), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
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
					go func() { defer c.Close(); _ = c.SetDeadline(time.Now().Add(40 * time.Second)); _, _ = io.Copy(c, c) }()
				}
			}()
			target := echo.Addr().(*net.TCPAddr).Port
			runner, err := engine.NewExecXrayRunner(xrayBinary(t))
			if err != nil {
				t.Fatal(err)
			}
			var execution engine.XrayRunner = runner
			if !live {
				execution = noLiveXrayRunner{runner}
			}
			root := t.TempDir()
			a, err := engine.NewXrayProcessAdapter(engine.XrayAdapterOptions{
				StagingDir: filepath.Join(root, "staging"), ActivePath: filepath.Join(root, "active.json"),
				Runner: execution, AutoStart: true, StatsAPIAddress: localAddress(freeLoopbackPort(t)),
			})
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close(context.Background())
			ports := []int{freeLoopbackPort(t), freeLoopbackPort(t), freeLoopbackPort(t)}
			// Linux Xray listeners use SO_REUSEPORT, so another Xray handler
			// can bind the same address successfully. An independent Go listener
			// does not opt into reuse and makes this failure portable.
			blocked, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer blocked.Close()
			blockedPort := blocked.Addr().(*net.TCPAddr).Port
			prepare := func(generation agentv1.NodeConfigGeneration, count int, conflict bool) engine.PreparedConfiguration {
				t.Helper()
				in, out, routes := []any{}, []any{}, []any{}
				for i := 0; i < count; i++ {
					port := ports[i]
					if conflict && i == count-1 {
						port = blockedPort
					}
					inTag, outTag := fmt.Sprintf("in-%d", i), fmt.Sprintf("out-%d", i)
					in = append(in, map[string]any{"tag": inTag, "listen": "127.0.0.1", "port": port, "protocol": "dokodemo-door", "settings": map[string]any{"address": "127.0.0.1", "port": target, "network": "tcp"}})
					out = append(out, map[string]any{"tag": outTag, "protocol": "freedom"})
					routes = append(routes, map[string]any{"type": "field", "inboundTag": []string{inTag}, "outboundTag": outTag})
				}
				data, _ := json.Marshal(map[string]any{"log": map[string]any{"loglevel": "none"}, "inbounds": in, "outbounds": out, "routing": map[string]any{"rules": routes}})
				p, err := a.Prepare(ctx, adapterDesired(t, generation, []agentv1.ConfigurationFragment{{GroupID: "independent", GroupRevision: agentv1.GroupRevision(generation), Engine: agentv1.EngineXray, Config: data}}))
				if err != nil {
					t.Fatal(err)
				}
				return p
			}
			first := prepare(1, 1, false)
			if err := a.Commit(ctx, first); err != nil {
				t.Fatal(err)
			}
			var connection net.Conn
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				connection, err = net.DialTimeout("tcp", localAddress(ports[0]), 100*time.Millisecond)
				if err == nil {
					break
				}
				time.Sleep(25 * time.Millisecond)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			check := func(c net.Conn) error {
				_ = c.SetDeadline(time.Now().Add(2 * time.Second))
				payload := "same-existing-tcp-session"
				if _, err := io.WriteString(c, payload); err != nil {
					return err
				}
				response := make([]byte, len(payload))
				_, err := io.ReadFull(c, response)
				if err == nil && string(response) != payload {
					return errors.New("wrong stream response")
				}
				return err
			}
			if err := check(connection); err != nil {
				t.Fatal(err)
			}
			pid, epoch := a.RuntimeMetrics().PID, a.CounterEpoch()
			second := prepare(2, 2, false)
			if err := a.Commit(ctx, second); err != nil {
				t.Fatal(err)
			}
			if !live {
				if a.CounterEpoch() == epoch || a.RuntimeMetrics().PID == pid {
					t.Fatal("legacy restart not reproduced")
				}
				if check(connection) == nil {
					t.Fatal("legacy path did not interrupt old stream")
				}
				return
			}
			checkOld := func() {
				t.Helper()
				if err := check(connection); err != nil {
					t.Fatalf("old TCP stream interrupted: %v", err)
				}
				if a.CounterEpoch() != epoch || a.RuntimeMetrics().PID != pid {
					t.Fatal("live operation replaced Xray")
				}
			}
			checkOld()
			added, err := net.DialTimeout("tcp", localAddress(ports[1]), 2*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer added.Close()
			if err := check(added); err != nil {
				t.Fatal(err)
			}
			third := prepare(3, 3, false)
			if err := a.Commit(ctx, third); err != nil {
				t.Fatal(err)
			}
			checkOld()
			if err := check(added); err != nil {
				t.Fatal("second rule stream interrupted")
			}
			if err := a.Rollback(ctx, &first); err != nil {
				t.Fatal(err)
			}
			if err := a.Verify(ctx, first); err != nil {
				t.Fatal(err)
			}
			checkOld()
			for _, port := range ports[1:] {
				c, err := net.DialTimeout("tcp", localAddress(port), 100*time.Millisecond)
				if err == nil {
					c.Close()
					t.Fatal("rolled back inbound remains open")
				}
			}
			// AddInbound applies the first new handler before a later bind fails.
			failed := prepare(4, 3, true)
			if err := a.Commit(ctx, failed); !errors.Is(err, engine.ErrXrayLiveUpdate) {
				t.Fatalf("partial bind failure: %v", err)
			}
			if err := a.Verify(ctx, first); err != nil {
				t.Fatal(err)
			}
			checkOld()
			c, err := net.DialTimeout("tcp", localAddress(ports[1]), 100*time.Millisecond)
			if err == nil {
				c.Close()
				t.Fatal("partial addition leaked listener")
			}
			if err := a.Commit(ctx, second); err != nil {
				t.Fatal("retry after compensation failed")
			}
			checkOld()
		})
	}
}
