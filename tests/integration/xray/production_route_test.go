package xray_test

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/forwarding/vlessconfig"
	"github.com/hongle/hl-panel/internal/control/protocolprobe"
	"github.com/hongle/hl-panel/internal/provisioning/vless"
)

// This writes the production compiler output unchanged. In particular it must
// never use privateConfig, whose loopback allowance belongs only to isolated
// general-purpose fixtures, not the bounded production redirect/probe routes.
func unmodifiedProductionConfig(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "production-config.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestXrayProductionDirectPreservesFixedPrivateTarget(t *testing.T) {
	testProductionRoutes(t, false)
}

func TestXrayProtocolProbeUsesDedicatedEchoRoute(t *testing.T) {
	testProductionRoutes(t, true)
}

func testProductionRoutes(t *testing.T, withProbe bool) {
	t.Helper()
	binary := xrayBinary(t)
	publicKey, privateKey, err := vless.GenerateRealityKeyPair(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath, _ := testCertificate(t)
	certificate, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	decoyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	decoy := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }), ErrorLog: log.New(io.Discard, "", 0)}
	go func() {
		_ = decoy.Serve(tls.NewListener(decoyListener, &tls.Config{Certificates: []tls.Certificate{certificate}}))
	}()
	t.Cleanup(func() { _ = decoy.Close() })
	// localhost can resolve to IPv4 or IPv6. Both loopback listeners serve the
	// same isolated origin; no wildcard bind or external DNS is needed.
	originListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	originPort := originListener.Addr().(*net.TCPAddr).Port
	origin := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "fixed-production-target")
	})}
	go func() { _ = origin.Serve(originListener) }()
	origin6, err := net.Listen("tcp6", fmt.Sprintf("[::1]:%d", originPort))
	if err != nil {
		origin.Close()
		t.Skip("dual-stack loopback is required to verify localhost DNS rotation")
	}
	go func() { _ = origin.Serve(origin6) }()
	t.Cleanup(func() { _ = origin.Close() })
	var alternativeRequests atomic.Int64
	alternative := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		alternativeRequests.Add(1)
		_, _ = io.WriteString(w, "arbitrary-client-target")
	}))
	t.Cleanup(alternative.Close)
	echoListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = echoListener.Close() })
	go func() {
		for {
			conn, err := echoListener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				challenge := make([]byte, 32)
				if _, err := io.ReadFull(conn, challenge); err == nil {
					_, _ = conn.Write(challenge)
				}
			}()
		}
	}()
	const customerUUID = "04f78a4e-bb88-4a65-86f0-9b7c53f11e0d"
	const probeUUID = "615db532-728a-4a34-8353-873b34d12de2"
	for _, host := range []string{"127.0.0.1", "localhost"} {
		t.Run(host, func(t *testing.T) {
			frontPort, clientPort := freeLoopbackPort(t), freeLoopbackPort(t)
			rule := forwarding.Rule{ID: "production-bounded-route", IngressProtocol: forwarding.IngressVLESSReality, Protocol: forwarding.ProtocolTCP, EgressMode: forwarding.EgressDirect, Targets: []forwarding.Target{{Host: host, Port: originPort}}}
			reality := vlessconfig.RealityProfile{ServerName: "localhost", Dest: decoyListener.Addr().String(), PrivateKey: privateKey, ShortIDs: []string{"01234567"}, Flow: "xtls-rprx-vision"}
			var config []byte
			if withProbe {
				config, err = vlessconfig.CompileDirectWithProbe(rule, vlessconfig.Identity{UUID: customerUUID}, reality, vlessconfig.Listener{Address: "127.0.0.1", Port: frontPort}, vlessconfig.ProbeIdentity{UUID: probeUUID, EchoPort: echoListener.Addr().(*net.TCPAddr).Port})
			} else {
				config, err = vlessconfig.CompileDirect(rule, vlessconfig.Identity{UUID: customerUUID}, reality, vlessconfig.Listener{Address: "127.0.0.1", Port: frontPort})
			}
			if err != nil {
				t.Fatal(err)
			}
			stop := startXray(t, binary, unmodifiedProductionConfig(t, config), localAddress(frontPort))
			defer stop()
			uri, err := vless.URI(vless.Profile{
				Endpoint: vless.Endpoint{Hostname: "127.0.0.1", Port: frontPort, Name: "production-bounded-route"}, Identity: vless.Identity{UUID: customerUUID, Flow: reality.Flow},
				Reality: &vless.RealityProfile{ServerName: reality.ServerName, Destination: reality.Dest, PublicKey: publicKey, ShortID: reality.ShortIDs[0], Fingerprint: "chrome", SpiderX: "/"},
			})
			if err != nil {
				t.Fatal(err)
			}
			stopClient := startXray(t, binary, unmodifiedProductionConfig(t, realityNativeClientFromURI(t, uri, clientPort)), localAddress(clientPort))
			defer stopClient()
			proxyURL, _ := url.Parse("socks5://" + localAddress(clientPort))
			transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
			for round := 1; round <= 3; round++ {
				// The customer requests a different host:port; production's hard
				// redirect must still deliver only the configured fixed origin.
				response, err := client.Get(alternative.URL)
				if err != nil {
					t.Fatalf("fixed production target transfer failed: %v", err)
				}
				body, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil || string(body) != "fixed-production-target" || alternativeRequests.Load() != 0 {
					t.Fatal("production fixed redirect was bypassed or blocked")
				}
			}
			if withProbe {
				// Even a probe request carrying another loopback destination is
				// forced to the dedicated EchoPort by its isolated identity.
				verifiedAt, err := protocolprobe.Check(context.Background(), protocolprobe.Spec{
					XrayBinary: binary, DialHost: "127.0.0.1", DialPort: frontPort, UUID: probeUUID,
					ServerName: "localhost", PublicKey: publicKey, ShortID: "01234567",
					EchoHost: "127.0.0.1", EchoPort: freeLoopbackPort(t),
				})
				if err != nil || verifiedAt.IsZero() {
					t.Fatalf("production isolated echo route failed: %v", err)
				}
			}
			t.Log("unmodified production configuration: three fixed-target transfers; arbitrary client target unreachable; dedicated probe verified when enabled")
		})
	}
}
