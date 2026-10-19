package xray_test

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/engine"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	"github.com/hongle/hl-panel/internal/provisioning/vless"
)

// A modern-core-only check misses the default Reality version floor applied to
// imported listeners. This fixture uses the exact core reported by the client.
func TestImportedRealityLegacyClient(t *testing.T) {
	legacy := os.Getenv("HL_TEST_XRAY_LEGACY_BINARY")
	if legacy == "" {
		t.Skip("set HL_TEST_XRAY_LEGACY_BINARY to verified Xray 25.10.15")
	}
	if !filepath.IsAbs(legacy) {
		t.Fatal("legacy core path must be absolute")
	}
	version, err := exec.Command(legacy, "version").Output()
	if err != nil || !strings.HasPrefix(string(version), "Xray 25.10.15 ") {
		t.Fatal("legacy fixture requires Xray 25.10.15")
	}
	binary := xrayBinary(t)
	publicKey, privateKey, err := vless.GenerateRealityKeyPair(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert, key, _ := testCertificate(t)
	certificate, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	decoy := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })}
	go func() {
		_ = decoy.Serve(tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{certificate}}))
	}()
	t.Cleanup(func() { _ = decoy.Close() })
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "legacy-reality-ok") }))
	t.Cleanup(target.Close)
	for _, tc := range []struct {
		name    string
		minimum *string
		allowed bool
	}{
		{"explicit_unrestricted", strPointer("0.0.0"), true},
		{"explicit_floor", strPointer("26.3.27"), false},
		{"empty", strPointer(""), true},
		{"omitted", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frontPort := freeLoopbackPort(t)
			reality := map[string]any{"target": listener.Addr().String(), "serverNames": []string{"localhost"}, "privateKey": privateKey, "shortIds": []string{"01234567"}}
			if tc.minimum != nil {
				reality["minClientVer"] = *tc.minimum
			}
			identity := "04f78a4e-bb88-4a65-86f0-9b7c53f11e0d"
			raw, _ := json.Marshal(map[string]any{
				"inbounds": []any{map[string]any{
					"tag": agentv1.NativeRealityStatsTagPrefix + "fixture", "listen": "127.0.0.1", "port": frontPort, "protocol": "vless",
					"settings":       map[string]any{"decryption": "none", "clients": []any{map[string]any{"id": identity, "flow": ""}}},
					"streamSettings": map[string]any{"network": "tcp", "security": "reality", "realitySettings": reality},
				}},
				"outbounds": []any{map[string]any{"tag": "fixture-direct", "protocol": "freedom"}},
			})
			compiled, err := engine.CompileXrayBundle(agentv1.ConfigurationBundle{SchemaVersion: engine.BundleSchemaVersion, Fragments: []agentv1.ConfigurationFragment{{Engine: agentv1.EngineXray, Config: raw}}})
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]any
			if err := json.Unmarshal(compiled, &document); err != nil {
				t.Fatal(err)
			}
			document["log"] = map[string]any{"loglevel": "warning", "access": "none"}
			compiled, _ = json.Marshal(document)
			startXray(t, binary, privateConfig(t, compiled), localAddress(frontPort))
			clientPort := freeLoopbackPort(t)
			query := url.Values{"type": {"tcp"}, "security": {"reality"}, "sni": {"localhost"}, "pbk": {publicKey}, "sid": {"01234567"}, "fp": {"chrome"}}
			uri := "vless://" + identity + "@" + localAddress(frontPort) + "?" + query.Encode()
			startXray(t, legacy, privateConfig(t, realityNativeClientFromURI(t, uri, clientPort)), localAddress(clientPort))
			proxy, _ := url.Parse("socks5://" + localAddress(clientPort))
			transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 4 * time.Second}
			response, requestErr := client.Get(target.URL)
			if response != nil {
				defer response.Body.Close()
			}
			if !tc.allowed {
				if requestErr == nil {
					t.Fatal("explicit minimum version was bypassed")
				}
				return
			}
			if requestErr != nil {
				t.Fatal("legacy client could not reach isolated target", requestErr)
			}
			body, err := io.ReadAll(response.Body)
			if err != nil || string(body) != "legacy-reality-ok" {
				t.Fatal("legacy client did not receive the complete response")
			}
		})
	}
}

func strPointer(value string) *string { return &value }
