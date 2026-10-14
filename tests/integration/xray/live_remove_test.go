package xray_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/engine"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestXrayDirectRemoveConnectionScope(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
			go func() { defer c.Close(); _ = c.SetDeadline(time.Now().Add(30 * time.Second)); _, _ = io.Copy(c, c) }()
		}
	}()
	runner, err := engine.NewExecXrayRunner(xrayBinary(t))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	apiAddress := localAddress(freeLoopbackPort(t))
	a, err := engine.NewXrayProcessAdapter(engine.XrayAdapterOptions{
		StagingDir: filepath.Join(root, "staging"), ActivePath: filepath.Join(root, "active.json"),
		Runner: runner, AutoStart: true, StatsAPIAddress: apiAddress,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(context.Background())
	ports := []int{freeLoopbackPort(t), freeLoopbackPort(t)}
	in, out, routes := []any{}, []any{}, []any{}
	for i := 0; i < 2; i++ {
		inTag, outTag := fmt.Sprintf("in-%d", i), fmt.Sprintf("out-%d", i)
		in = append(in, map[string]any{"tag": inTag, "listen": "127.0.0.1", "port": ports[i], "protocol": "dokodemo-door", "settings": map[string]any{"address": "127.0.0.1", "port": echo.Addr().(*net.TCPAddr).Port, "network": "tcp"}})
		out = append(out, map[string]any{"tag": outTag, "protocol": "freedom"})
		routes = append(routes, map[string]any{"type": "field", "inboundTag": []string{inTag}, "outboundTag": outTag})
	}
	data, _ := json.Marshal(map[string]any{"log": map[string]any{"loglevel": "none"}, "inbounds": in, "outbounds": out, "routing": map[string]any{"rules": routes}})
	p, err := a.Prepare(ctx, adapterDesired(t, 1, []agentv1.ConfigurationFragment{{GroupID: "independent", GroupRevision: 1, Engine: agentv1.EngineXray, Config: data}}))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Commit(ctx, p); err != nil {
		t.Fatal(err)
	}
	streams := []net.Conn{}
	for _, port := range ports {
		var c net.Conn
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			c, err = net.DialTimeout("tcp", localAddress(port), 100*time.Millisecond)
			if err == nil {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		streams = append(streams, c)
	}
	check := func(c net.Conn) error {
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := io.WriteString(c, "scope"); err != nil {
			return err
		}
		response := make([]byte, 5)
		_, err := io.ReadFull(c, response)
		return err
	}
	for _, c := range streams {
		if err := check(c); err != nil {
			t.Fatal(err)
		}
	}
	pid, epoch := a.RuntimeMetrics().PID, a.CounterEpoch()
	document, _ := json.Marshal(map[string]any{"inbounds": []any{in[1]}})
	if _, err := runner.API(ctx, apiAddress, "rmi", document); err != nil {
		t.Fatal(err)
	}
	if err := check(streams[0]); err != nil {
		t.Fatalf("unrelated stream interrupted: %v", err)
	}
	if a.RuntimeMetrics().PID != pid || a.CounterEpoch() != epoch {
		t.Fatal("API removal restarted process")
	}
	t.Logf("removed inbound's established stream continues=%v", check(streams[1]) == nil)
	c, err := net.DialTimeout("tcp", localAddress(ports[1]), 100*time.Millisecond)
	if err == nil {
		c.Close()
		t.Fatal("removed listener remains open")
	}
}
