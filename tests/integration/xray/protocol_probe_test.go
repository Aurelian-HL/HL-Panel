package xray_test

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"io"
	"log"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/protocolprobe"
	"github.com/hongle/hl-panel/internal/provisioning/vless"
)

func TestProtocolProbeRealRealityVisionEchoLoopback(t *testing.T) {
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
	decoyTLS := tls.NewListener(decoyListener, &tls.Config{Certificates: []tls.Certificate{certificate}})
	decoyServer := &http.Server{
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusOK)
		}),
		ErrorLog: log.New(io.Discard, "", 0),
	}
	go func() { _ = decoyServer.Serve(decoyTLS) }()
	t.Cleanup(func() { _ = decoyServer.Close() })

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

	frontPort := freeLoopbackPort(t)
	const uuid = "04f78a4e-bb88-4a65-86f0-9b7c53f11e0d"
	serverConfig, err := vless.Compile(vless.Profile{
		Endpoint: vless.Endpoint{Hostname: "127.0.0.1", Port: frontPort, Name: "probe-loopback"},
		Identity: vless.Identity{UUID: uuid, Flow: "xtls-rprx-vision"},
		Reality: &vless.RealityProfile{
			ServerName: "localhost", PublicKey: publicKey, PrivateKey: privateKey,
			ShortID: "01234567", Destination: decoyTLS.Addr().String(), Fingerprint: "chrome", SpiderX: "/",
		},
	}, vless.Listener{Address: "127.0.0.1", Port: frontPort}, vless.Egress{Mode: vless.EgressDirect})
	if err != nil {
		t.Fatal(err)
	}
	startXray(t, binary, privateConfig(t, serverConfig), localAddress(frontPort))
	spec := protocolprobe.Spec{
		XrayBinary: binary, DialHost: "127.0.0.1", DialPort: frontPort,
		UUID: uuid, ServerName: "localhost", PublicKey: publicKey, ShortID: "01234567",
		EchoHost: "127.0.0.1", EchoPort: echoListener.Addr().(*net.TCPAddr).Port,
	}
	verifiedAt, err := protocolprobe.Check(context.Background(), spec)
	if err != nil || verifiedAt.IsZero() {
		t.Fatalf("real Reality Vision echo probe failed: %v", err)
	}
	spec.UUID = "615db532-728a-4a34-8353-873b34d12de2"
	if _, err := protocolprobe.Check(context.Background(), spec); err == nil {
		t.Fatal("wrong VLESS identity passed the real protocol probe")
	}
}
