package xray_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/engine"
	"github.com/hongle/hl-panel/internal/gateway/healthprobe"
	"github.com/hongle/hl-panel/internal/gateway/tcpproxy"
	"github.com/hongle/hl-panel/internal/provisioning/vless"
)

// This instrumentation delegates all selection and reservations to the real
// router. It only records which server accepted each connection for assertions.
type observedSelector struct {
	router *engine.EndpointRouter
	mu     sync.Mutex
	counts map[string]int
}

func (s *observedSelector) Select() (tcpproxy.Backend, error) {
	endpoint, err := s.router.Select()
	if err != nil {
		return tcpproxy.Backend{}, err
	}
	s.mu.Lock()
	s.counts[endpoint.ID]++
	s.mu.Unlock()
	return tcpproxy.Backend{ID: endpoint.ID, Address: endpoint.Address}, nil
}
func (s *observedSelector) Release(id string) error { return s.router.Release(id) }
func (s *observedSelector) count(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts[id]
}

func TestSingleVLESSURIThroughTwoRealXrayServers(t *testing.T) {
	binary := xrayBinary(t)
	for _, test := range []struct {
		name, flow string
		mode       vless.EgressMode
	}{
		{"direct_standard", "", vless.EgressDirect},
		{"direct_vision", "xtls-rprx-vision", vless.EgressDirect},
		{"socks_landing", "", vless.EgressSOCKSSingle},
	} {
		t.Run(test.name, func(t *testing.T) { verifySingleEndpoint(t, binary, test.flow, test.mode) })
	}
}

func verifySingleEndpoint(t *testing.T, binary, flow string, mode vless.EgressMode) {
	egress := vless.Egress{Mode: mode}
	var landingObserver *observedSelector
	if mode == vless.EgressSOCKSSingle {
		egress, landingObserver = startSOCKSLanding(t, binary)
	}
	certificate, key, certificatePEM := testCertificate(t)
	front, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { front.Close() })
	profile := vless.Profile{
		Endpoint: vless.Endpoint{Hostname: "127.0.0.1", Port: front.Addr().(*net.TCPAddr).Port, Name: "one-service"},
		Identity: vless.Identity{UUID: "04f78a4e-bb88-4a65-86f0-9b7c53f11e0d", Flow: flow}, TLS: vless.TLSProfile{ServerName: "localhost"},
	}
	uri, err := vless.URI(profile)
	if err != nil {
		t.Fatal(err)
	}
	var members []engine.Endpoint
	var stopServers []func()
	for index := 0; index < 2; index++ {
		port := freeLoopbackPort(t)
		configuration, err := vless.Compile(profile, vless.Listener{
			Address: "127.0.0.1", Port: port, CertificateFile: certificate, PrivateKeyFile: key,
		}, egress)
		if err != nil {
			t.Fatal(err)
		}
		stopServers = append(stopServers, startXray(t, binary, privateConfig(t, configuration), localAddress(port)))
		members = append(members, engine.Endpoint{ID: fmt.Sprintf("backend-%d", index), Address: localAddress(port),
			Weight: 1, Status: engine.EndpointReady, HealthLeaseExpiresAt: time.Now().Add(time.Minute)})
	}
	router, err := engine.NewEndpointRouter(engine.SelectionPolicyWeightedRoundRobin)
	if err != nil {
		t.Fatal(err)
	}
	const membershipRevision = uint64(1)
	selector := &observedSelector{router: router, counts: make(map[string]int)}
	gateway, err := tcpproxy.New(tcpproxy.Config{Listener: front, Selector: selector})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	probeErrors := make(chan error, 8)
	monitor, err := healthprobe.NewController(router, healthprobe.Config{
		Interval: 50 * time.Millisecond, Timeout: 40 * time.Millisecond,
		FailureThreshold: 2, SuccessThreshold: 2, MaxConcurrency: 2,
	}, func(_ healthprobe.Verdict, err error) {
		if err != nil {
			select {
			case probeErrors <- err:
			default:
			}
		}
	})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := monitor.ReplaceVersionedMembership(membershipRevision, members); err != nil {
		cancel()
		t.Fatal(err)
	}
	probeDone := make(chan struct{})
	go func() {
		defer close(probeDone)
		monitor.Run(ctx)
	}()
	done := make(chan error, 1)
	go func() { done <- gateway.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		gateway.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("gateway shutdown timed out")
		}
		select {
		case <-probeDone:
		case <-time.After(5 * time.Second):
			t.Error("reachability monitor shutdown timed out")
		}
	})
	socksPort := freeLoopbackPort(t)
	clientConfig := nativeClientFromURI(t, uri, socksPort, certificatePEM)
	startXray(t, binary, privateConfig(t, clientConfig), localAddress(socksPort))
	var targetRequests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetRequests.Add(1)
		_, _ = w.Write([]byte("vless-direct-ok"))
	}))
	defer target.Close()
	proxyURL, _ := url.Parse("socks5://" + localAddress(socksPort))
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	request := func() {
		t.Helper()
		response, err := client.Get(target.URL)
		if err != nil {
			t.Fatalf("real VLESS request failed: %v", err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil || string(body) != "vless-direct-ok" {
			t.Fatal("real VLESS response mismatch")
		}
	}
	for range 6 {
		request()
	}
	if selector.count("backend-0") != 3 || selector.count("backend-1") != 3 {
		t.Fatal("connections did not reach both Xray servers evenly")
	}
	// Stop the real backend. Only the independent TCP monitor may veto it;
	// membership and the authoritative lease remain unchanged.
	stopServers[0]()
	deadline := time.Now().Add(5 * time.Second)
	for {
		blocked, err := router.LocallyBlocked("backend-0")
		if err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stopped real Xray server was not automatically excluded")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for range 3 {
		request()
	}
	if selector.count("backend-0") != 3 || selector.count("backend-1") != 6 {
		t.Fatal("failed server remained in new-connection pool")
	}
	after, _ := vless.URI(profile)
	if uri != after {
		t.Fatal("customer URI changed during failover")
	}
	if landingObserver != nil && landingObserver.count("landing") != 9 {
		t.Fatal("VLESS requests bypassed configured SOCKS5 landing")
	}
	select {
	case <-probeErrors:
		t.Fatal("reachability observation was rejected")
	default:
	}
	t.Log("one TLS VLESS URI: six real requests balanced 3:3; TCP monitor automatically excluded the stopped server; three more requests succeeded without changing membership or URI")
	_, _, wrongCertificate := testCertificate(t)
	wrongName, _ := url.Parse(uri)
	wrongQuery := wrongName.Query()
	wrongQuery.Set("sni", "wrong.example.test")
	wrongName.RawQuery = wrongQuery.Encode()
	wrongIdentity, _ := url.Parse(uri)
	wrongIdentity.User = url.User("615db532-728a-4a34-8353-873b34d12de2")
	for _, invalid := range []struct{ name, link, ca string }{
		{"wrong_ca", uri, wrongCertificate}, {"wrong_sni", wrongName.String(), certificatePEM},
		{"wrong_identity", wrongIdentity.String(), certificatePEM},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			port := freeLoopbackPort(t)
			startXray(t, binary, privateConfig(t, nativeClientFromURI(t, invalid.link, port, invalid.ca)), localAddress(port))
			proxy, _ := url.Parse("socks5://" + localAddress(port))
			transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
			response, err := client.Get(target.URL)
			if response != nil {
				response.Body.Close()
			}
			if err == nil {
				t.Fatal("invalid TLS or VLESS credentials unexpectedly reached target")
			}
		})
	}
	if egress.SOCKS5 != nil {
		// Invalid landing credentials must fail the request, never silently
		// fall back to DIRECT and expose a different outbound address.
		badUpstream := *egress.SOCKS5
		badUpstream.Password = "incorrect-integration-password"
		port := freeLoopbackPort(t)
		configuration, err := vless.Compile(profile, vless.Listener{Address: "127.0.0.1", Port: port, CertificateFile: certificate, PrivateKeyFile: key}, vless.Egress{Mode: mode, SOCKS5: &badUpstream})
		if err != nil {
			t.Fatal(err)
		}
		startXray(t, binary, privateConfig(t, configuration), localAddress(port))
		if err := monitor.ReplaceVersionedMembership(2, []engine.Endpoint{{ID: "bad-landing", Address: localAddress(port), Weight: 1, Status: engine.EndpointReady, HealthLeaseExpiresAt: time.Now().Add(time.Minute)}}); err != nil {
			t.Fatal(err)
		}
		response, err := client.Get(target.URL)
		if response != nil {
			response.Body.Close()
		}
		if err == nil || targetRequests.Load() != 9 {
			t.Fatal("invalid SOCKS5 credentials bypassed configured egress")
		}
		t.Log("all nine requests used authenticated SOCKS5 landing; incorrect landing credentials failed without DIRECT fallback")
	}
}

func nativeClientFromURI(t *testing.T, uri string, port int, certificate string) []byte {
	t.Helper()
	link, err := url.Parse(uri)
	if err != nil {
		t.Fatal("invalid generated URI")
	}
	query := link.Query()
	if link.Scheme != "vless" || link.User == nil || query.Get("type") != "tcp" || query.Get("security") != "tls" || query.Get("encryption") != "none" || query.Get("alpn") != "http/1.1" {
		t.Fatal("generated URI protocol parameters are invalid")
	}
	if flow := query.Get("flow"); flow != "" && flow != "xtls-rprx-vision" {
		t.Fatal("generated URI has unsupported flow")
	}
	_, frontPort, err := net.SplitHostPort(link.Host)
	if err != nil {
		t.Fatal("invalid generated endpoint")
	}
	var numericPort int
	if _, err := fmt.Sscan(frontPort, &numericPort); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{
		"log":      map[string]any{"loglevel": "warning", "access": "none"},
		"inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": port, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false}}},
		"outbounds": []any{map[string]any{
			"protocol": "vless", "settings": map[string]any{"vnext": []any{map[string]any{
				"address": link.Hostname(), "port": numericPort, "users": []any{map[string]any{"id": link.User.Username(), "encryption": query.Get("encryption"), "flow": query.Get("flow")}},
			}}},
			"streamSettings": map[string]any{"network": query.Get("type"), "security": query.Get("security"), "tlsSettings": map[string]any{
				"serverName": query.Get("sni"), "alpn": strings.Split(query.Get("alpn"), ","), "allowInsecure": false,
				// Xray on Windows only loads a configured private CA when this
				// flag is set. Trust our ephemeral test CA, never skip verification.
				"disableSystemRoot": true,
				"certificates":      []any{map[string]any{"usage": "verify", "certificate": strings.Split(strings.TrimSpace(certificate), "\n")}},
			}},
		}},
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
