package xray_test

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/provisioning/vless"
)

// TestSingleVLESSRealityVisionLoopback proves the customer-facing Reality
// handshake against the pinned Xray build. It deliberately uses one direct
// loopback listener; scheduling and failure removal are covered independently
// by the multi-backend test and are not repeated against any remote host.
func TestSingleVLESSRealityVisionLoopback(t *testing.T) {
	binary := xrayBinary(t)
	publicKey, privateKey, err := vless.GenerateRealityKeyPair(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	frontPort := freeLoopbackPort(t)
	decoyCert, decoyKey, _ := testCertificate(t)
	decoyCertificate, err := tls.LoadX509KeyPair(decoyCert, decoyKey)
	if err != nil {
		t.Fatal(err)
	}
	decoyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	decoyTLSListener := tls.NewListener(decoyListener, &tls.Config{Certificates: []tls.Certificate{decoyCertificate}})
	decoyServer := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("reality-decoy-ok"))
	})}
	go func() { _ = decoyServer.Serve(decoyTLSListener) }()
	t.Cleanup(func() {
		_ = decoyServer.Close()
		_ = decoyTLSListener.Close()
	})
	profile := vless.Profile{
		Endpoint: vless.Endpoint{Hostname: "127.0.0.1", Port: frontPort, Name: "loopback-reality"},
		Identity: vless.Identity{UUID: "04f78a4e-bb88-4a65-86f0-9b7c53f11e0d", Flow: "xtls-rprx-vision"},
		Reality: &vless.RealityProfile{
			ServerName: "localhost", PublicKey: publicKey, PrivateKey: privateKey,
			ShortID: "01234567", Destination: decoyTLSListener.Addr().String(), Fingerprint: "chrome", SpiderX: "/",
		},
	}
	serverConfig, err := vless.Compile(profile, vless.Listener{Address: "127.0.0.1", Port: frontPort}, vless.Egress{Mode: vless.EgressDirect})
	if err != nil {
		t.Fatal(err)
	}
	startXray(t, binary, privateConfig(t, serverConfig), localAddress(frontPort))
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("reality-loopback-ok"))
	}))
	defer target.Close()

	uri, err := vless.URI(profile)
	if err != nil {
		t.Fatal(err)
	}
	requestThrough := func(name, link string) error {
		clientPort := freeLoopbackPort(t)
		startXray(t, binary, privateConfig(t, realityNativeClientFromURI(t, link, clientPort)), localAddress(clientPort))
		proxyURL, _ := url.Parse("socks5://" + localAddress(clientPort))
		transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
		response, requestErr := client.Get(target.URL)
		if response != nil {
			defer response.Body.Close()
		}
		if requestErr != nil {
			return fmt.Errorf("%s: %w", name, requestErr)
		}
		body, readErr := io.ReadAll(response.Body)
		if readErr != nil || string(body) != "reality-loopback-ok" {
			return fmt.Errorf("%s: unexpected response", name)
		}
		return nil
	}
	if err := requestThrough("valid Reality Vision", uri); err != nil {
		t.Fatal(err)
	}

	invalidCases := []struct {
		name   string
		mutate func(url.Values)
	}{
		{name: "wrong SNI", mutate: func(query url.Values) { query.Set("sni", "wrong.example.test") }},
		{name: "wrong short id", mutate: func(query url.Values) { query.Set("sid", "deadbeef") }},
	}
	for _, invalid := range invalidCases {
		invalid := invalid
		t.Run(invalid.name, func(t *testing.T) {
			parsed, err := url.Parse(uri)
			if err != nil {
				t.Fatal(err)
			}
			query := parsed.Query()
			invalid.mutate(query)
			parsed.RawQuery = query.Encode()
			if err := requestThrough(invalid.name, parsed.String()); err == nil {
				t.Fatal("invalid Reality parameters unexpectedly reached target")
			}
		})
	}
	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatal(err)
	}
	parsed.User = url.User("615db532-728a-4a34-8353-873b34d12de2")
	if err := requestThrough("wrong UUID", parsed.String()); err == nil {
		t.Fatal("invalid VLESS identity unexpectedly reached target")
	}
}

func realityNativeClientFromURI(t *testing.T, uri string, port int) []byte {
	t.Helper()
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Scheme != "vless" || parsed.User == nil {
		t.Fatal("invalid generated Reality URI")
	}
	query := parsed.Query()
	if query.Get("security") != "reality" || query.Get("type") != "tcp" || query.Get("flow") != "xtls-rprx-vision" || query.Get("pbk") == "" || query.Get("sid") == "" {
		t.Fatal("generated Reality URI is incomplete")
	}
	_, frontPort, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	var numericPort int
	if _, err := fmt.Sscan(frontPort, &numericPort); err != nil {
		t.Fatal(err)
	}
	configuration := map[string]any{
		"log": map[string]any{"loglevel": "warning", "access": "none"},
		"inbounds": []any{map[string]any{
			"listen": "127.0.0.1", "port": port, "protocol": "socks",
			"settings": map[string]any{"auth": "noauth", "udp": false},
		}},
		"outbounds": []any{map[string]any{
			"protocol": "vless",
			"settings": map[string]any{"vnext": []any{map[string]any{
				"address": parsed.Hostname(), "port": numericPort,
				"users": []any{map[string]any{"id": parsed.User.Username(), "encryption": "none", "flow": query.Get("flow")}},
			}}},
			"streamSettings": map[string]any{
				"network": "tcp", "security": "reality",
				"realitySettings": map[string]any{
					"serverName": query.Get("sni"), "publicKey": query.Get("pbk"),
					"shortId": query.Get("sid"), "fingerprint": query.Get("fp"),
					"spiderX": query.Get("spx"),
				},
			},
		}},
	}
	data, err := json.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "privateKey") {
		t.Fatal("Reality client configuration unexpectedly contains a server private key")
	}
	return data
}
