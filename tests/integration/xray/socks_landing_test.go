package xray_test

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/engine"
	"github.com/hongle/hl-panel/internal/gateway/tcpproxy"
	"github.com/hongle/hl-panel/internal/provisioning/vless"
)

func startSOCKSLanding(t *testing.T, binary string) (vless.Egress, *observedSelector) {
	t.Helper()
	port := freeLoopbackPort(t)
	upstream := &vless.SOCKS5Upstream{Hostname: "127.0.0.1", Username: "integration-landing", Password: "disposable-integration-password"}
	configuration, err := json.Marshal(map[string]any{
		"log": map[string]any{"loglevel": "warning", "access": "none"},
		"inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": port, "protocol": "socks", "settings": map[string]any{
			"auth": "password", "accounts": []any{map[string]any{"user": upstream.Username, "pass": upstream.Password}}, "udp": false,
		}}},
		"outbounds": []any{map[string]any{"protocol": "freedom"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	startXray(t, binary, privateConfig(t, configuration), localAddress(port))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	upstream.Port = listener.Addr().(*net.TCPAddr).Port
	router, err := engine.NewEndpointRouter(engine.SelectionPolicyWeightedRoundRobin)
	if err != nil {
		t.Fatal(err)
	}
	if err := router.ReplaceMembership([]engine.Endpoint{{ID: "landing", Address: localAddress(port), Weight: 1, Status: engine.EndpointReady, HealthLeaseExpiresAt: time.Now().Add(time.Minute)}}); err != nil {
		t.Fatal(err)
	}
	observer := &observedSelector{router: router, counts: make(map[string]int)}
	server, err := tcpproxy.New(tcpproxy.Config{Listener: listener, Selector: observer})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		server.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("landing observer shutdown timed out")
		}
	})
	return vless.Egress{Mode: vless.EgressSOCKSSingle, SOCKS5: upstream}, observer
}
