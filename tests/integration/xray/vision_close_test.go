package xray_test

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/provisioning/vless"
	"golang.org/x/net/proxy"
)

// Vision bypasses its outer TLS layer after recognizing inner TLS 1.3. The
// outer layer must not append its own close_notify to the inner byte stream.
// A loopback origin closes raw TCP after its response to expose that defect.
func TestVisionTLS13AbruptCloseDoesNotCorruptInnerStream(t *testing.T) {
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
	origin, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = origin.Close() })
	payload := bytes.Repeat([]byte("vision-inner-tls-"), 512)
	go func() {
		for {
			raw, acceptErr := origin.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer raw.Close()
				_ = raw.SetDeadline(time.Now().Add(10 * time.Second))
				secure := tls.Server(raw, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
				reader := bufio.NewReader(secure)
				for {
					line, readErr := reader.ReadString('\n')
					if readErr != nil {
						return
					}
					if line == "\r\n" {
						break
					}
				}
				response := append([]byte(fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", len(payload))), payload...)
				_, _ = secure.Write(response)
				// Closing raw, rather than secure, deliberately omits the origin's
				// TLS close_notify. The proxy must preserve this EOF unchanged.
			}()
		}
	}()
	frontPort, clientPort := freeLoopbackPort(t), freeLoopbackPort(t)
	profile := vless.Profile{
		Endpoint: vless.Endpoint{Hostname: "127.0.0.1", Port: frontPort, Name: "vision-close-loopback"},
		Identity: vless.Identity{UUID: "04f78a4e-bb88-4a65-86f0-9b7c53f11e0d", Flow: "xtls-rprx-vision"},
		TLS:      vless.TLSProfile{ServerName: "localhost"},
	}
	serverConfig, err := vless.Compile(profile, vless.Listener{Address: "127.0.0.1", Port: frontPort, CertificateFile: certPath, PrivateKeyFile: keyPath}, vless.Egress{Mode: vless.EgressDirect})
	if err != nil {
		t.Fatal(err)
	}
	startXray(t, binaryPath, privateConfig(t, serverConfig), localAddress(frontPort))
	uri, err := vless.URI(profile)
	if err != nil {
		t.Fatal(err)
	}
	startXray(t, binaryPath, privateConfig(t, nativeClientFromURI(t, uri, clientPort, certPEM)), localAddress(clientPort))
	dialer, err := proxy.SOCKS5("tcp", localAddress(clientPort), nil, &net.Dialer{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		version  uint16
		viaProxy bool
	}{
		{"direct_tls13", tls.VersionTLS13, false},
		{"vision_tls12_control", tls.VersionTLS12, true},
		{"vision_tls13", tls.VersionTLS13, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			knownOldDefect := knownVisionCloseDefect() && test.viaProxy && test.version == tls.VersionTLS13
			rounds, reproduced := 3, 0
			if knownOldDefect {
				rounds = 8
			}
			for round := 1; round <= rounds; round++ {
				var raw net.Conn
				if test.viaProxy {
					raw, err = dialer.Dial("tcp", origin.Addr().String())
				} else {
					raw, err = net.DialTimeout("tcp", origin.Addr().String(), 5*time.Second)
				}
				if err != nil {
					t.Fatal("fixture dial failed")
				}
				_ = raw.SetDeadline(time.Now().Add(10 * time.Second))
				tap := &tlsRecordTap{Conn: raw}
				secure := tls.Client(tap, &tls.Config{ServerName: "localhost", RootCAs: pool, MinVersion: test.version, MaxVersion: test.version})
				if err := secure.Handshake(); err != nil {
					raw.Close()
					t.Fatalf("inner TLS handshake failed: %v", err)
				}
				if _, err := io.WriteString(secure, "GET / HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n"); err != nil {
					raw.Close()
					t.Fatal("inner request write failed")
				}
				body, readErr := io.ReadAll(secure)
				_ = raw.Close()
				parts := bytes.SplitN(body, []byte("\r\n\r\n"), 2)
				if len(parts) != 2 || !bytes.Equal(parts[1], payload) {
					t.Fatal("inner TLS payload changed or was truncated")
				}
				if knownOldDefect && readErr != nil {
					if !strings.Contains(readErr.Error(), "bad record MAC") || !tap.endsWithEncryptedAlert() {
						t.Fatalf("old baseline returned unexpected outer alert corruption: error=%v records=%v", readErr, tap.lengths)
					}
					reproduced++
					t.Logf("round %d: old core reproduced bad record MAC with an extra 19-byte encrypted alert", round)
				} else if readErr != nil || (test.version == tls.VersionTLS13 && tap.endsWithEncryptedAlert()) {
					t.Fatalf("proxy corrupted inner TLS close: error=%v records=%v", readErr, tap.lengths)
				} else {
					t.Logf("round %d: full payload and clean raw EOF; no outer encrypted alert", round)
				}
			}
			if knownOldDefect && reproduced == 0 {
				t.Fatalf("old baseline did not reproduce the known close defect in %d rounds", rounds)
			}
		})
	}
}

type tlsRecordTap struct {
	net.Conn
	pending []byte
	lengths []int
	types   []byte
}

func (c *tlsRecordTap) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.pending = append(c.pending, p[:n]...)
	for len(c.pending) >= 5 {
		length := int(binary.BigEndian.Uint16(c.pending[3:5]))
		if len(c.pending) < 5+length {
			break
		}
		c.lengths = append(c.lengths, length)
		c.types = append(c.types, c.pending[0])
		c.pending = c.pending[5+length:]
	}
	return n, err
}

func (c *tlsRecordTap) endsWithEncryptedAlert() bool {
	return len(c.lengths) > 0 && c.types[len(c.types)-1] == 23 && c.lengths[len(c.lengths)-1] == 19
}
