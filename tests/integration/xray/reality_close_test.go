package xray_test

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/provisioning/vless"
	"golang.org/x/net/proxy"
)

// TestRealityVisionTLS13AbruptCloseDoesNotCorruptInnerStream repeats the
// close-notify regression through the actual customer-facing Reality listener.
// The origin sends a complete response and then closes its raw TCP socket.
func TestRealityVisionTLS13AbruptCloseDoesNotCorruptInnerStream(t *testing.T) {
	binaryPath := xrayBinary(t)
	certPath, keyPath, certPEM := testCertificate(t)
	certificate, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(certPEM)) {
		t.Fatal("fixture certificate could not be loaded")
	}

	decoyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	decoyTLS := tls.NewListener(decoyListener, &tls.Config{Certificates: []tls.Certificate{certificate}})
	decoy := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "reality-decoy")
	})}
	go func() { _ = decoy.Serve(decoyTLS) }()
	t.Cleanup(func() {
		_ = decoy.Close()
		_ = decoyTLS.Close()
	})

	originListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = originListener.Close() })
	payload := bytes.Repeat([]byte("reality-inner-tls-"), 512)
	go func() {
		for {
			raw, acceptErr := originListener.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				_ = raw.SetDeadline(time.Now().Add(10 * time.Second))
				secure := tls.Server(raw, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
				reader := bufio.NewReader(secure)
				for {
					line, readErr := reader.ReadString('\n')
					if readErr != nil {
						_ = raw.Close()
						return
					}
					if line == "\r\n" {
						break
					}
				}
				response := append([]byte(fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", len(payload))), payload...)
				_, _ = secure.Write(response)
				// Intentionally omit the origin TLS close_notify.
				_ = raw.Close()
			}()
		}
	}()

	frontPort, clientPort := freeLoopbackPort(t), freeLoopbackPort(t)
	publicKey, privateKey, err := vless.GenerateRealityKeyPair(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	profile := vless.Profile{
		Endpoint: vless.Endpoint{Hostname: "127.0.0.1", Port: frontPort, Name: "reality-close-loopback"},
		Identity: vless.Identity{UUID: "04f78a4e-bb88-4a65-86f0-9b7c53f11e0d", Flow: "xtls-rprx-vision"},
		Reality: &vless.RealityProfile{
			ServerName: "localhost", PublicKey: publicKey, PrivateKey: privateKey,
			ShortID: "01234567", Destination: decoyListener.Addr().String(), Fingerprint: "chrome", SpiderX: "/",
		},
	}
	serverConfig, err := vless.Compile(profile, vless.Listener{Address: "127.0.0.1", Port: frontPort}, vless.Egress{Mode: vless.EgressDirect})
	if err != nil {
		t.Fatal(err)
	}
	startXray(t, binaryPath, privateConfig(t, serverConfig), localAddress(frontPort))
	uri, err := vless.URI(profile)
	if err != nil {
		t.Fatal(err)
	}
	startXray(t, binaryPath, privateConfig(t, realityNativeClientFromURI(t, uri, clientPort)), localAddress(clientPort))
	dialer, err := proxy.SOCKS5("tcp", localAddress(clientPort), nil, &net.Dialer{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	for round := 1; round <= 3; round++ {
		raw, err := dialer.Dial("tcp", originListener.Addr().String())
		if err != nil {
			t.Fatal("Reality fixture dial failed")
		}
		_ = raw.SetDeadline(time.Now().Add(10 * time.Second))
		tap := &tlsRecordTap{Conn: raw}
		secure := tls.Client(tap, &tls.Config{ServerName: "localhost", RootCAs: pool, MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13})
		if err := secure.Handshake(); err != nil {
			_ = raw.Close()
			t.Fatal("inner TLS handshake failed", err)
		}
		if _, err := io.WriteString(secure, "GET / HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n"); err != nil {
			_ = raw.Close()
			t.Fatal("inner request write failed", err)
		}
		body, readErr := io.ReadAll(secure)
		_ = raw.Close()
		parts := bytes.SplitN(body, []byte("\r\n\r\n"), 2)
		if len(parts) != 2 || !bytes.Equal(parts[1], payload) {
			t.Fatal("Reality inner TLS payload changed or was truncated")
		}
		knownOldDefect := strings.HasPrefix(testExpectedVersion(), "26.3.27")
		if knownOldDefect {
			if readErr == nil || !strings.Contains(readErr.Error(), "bad record MAC") || !tap.endsWithEncryptedAlert() {
				t.Fatalf("old Reality baseline did not reproduce expected corruption: error=%v records=%v", readErr, tap.lengths)
			}
			t.Logf("round %d: old Reality path reproduced bad record MAC with extra encrypted alert", round)
		} else if readErr != nil || tap.endsWithEncryptedAlert() {
			t.Fatalf("Reality path corrupted inner TLS close: error=%v records=%v", readErr, tap.lengths)
		} else {
			t.Logf("round %d: complete Reality payload and clean EOF", round)
		}
	}
}

func testExpectedVersion() string {
	if version := strings.TrimSpace(os.Getenv("NYVP_XRAY_EXPECTED_VERSION")); version != "" {
		return version
	}
	return "26.10.10"
}
