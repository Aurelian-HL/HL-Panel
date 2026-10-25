package xray_test

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/protocolprobe"
	"github.com/hongle/hl-panel/internal/provisioning/vless"
)

func TestProtocolProbeRealRealityVisionEchoLoopback(t *testing.T) {
	testProtocolProbeLoopback(t, false)
}

func TestSharedProtocolProbeRealRealityInventoryLoopback(t *testing.T) {
	testProtocolProbeLoopback(t, true)
}

func testProtocolProbeLoopback(t *testing.T, shared bool) {
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
	var challenges atomic.Int64
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
					challenges.Add(1)
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
	// A large inventory exercises distinct authenticated routes, not duplicate
	// EvidenceKeys that would collapse to the same shared outbound.
	var specs []protocolprobe.Spec
	if shared {
		var config map[string]any
		if err := json.Unmarshal(serverConfig, &config); err != nil {
			t.Fatal(err)
		}
		settings := config["inbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any)
		var clients []any
		for i := 0; i < 2304; i++ {
			id := fmt.Sprintf("00000000-0000-4000-8000-%012x", i+1)
			clients = append(clients, map[string]any{"id": id, "flow": "xtls-rprx-vision"})
			specs = append(specs, protocolprobe.Spec{XrayBinary: binary,
				DialHost: "127.0.0.1", DialPort: frontPort, UUID: id,
				ServerName: "localhost", PublicKey: publicKey, ShortID: "01234567",
				EchoHost: "127.0.0.1", EchoPort: echoListener.Addr().(*net.TCPAddr).Port})
		}
		settings["clients"] = clients
		serverConfig, err = json.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
	}
	startXray(t, binary, privateConfig(t, serverConfig), localAddress(frontPort))
	if shared {
		testSharedInventory(t, binary, specs, &challenges)
		return
	}
	spec := protocolprobe.Spec{
		EvidenceKey: "isolated-rule-fragment-one",
		XrayBinary:  binary, DialHost: "127.0.0.1", DialPort: frontPort,
		UUID: uuid, ServerName: "localhost", PublicKey: publicKey, ShortID: "01234567",
		EchoHost: "127.0.0.1", EchoPort: echoListener.Addr().(*net.TCPAddr).Port,
	}
	verifiedAt, err := protocolprobe.Check(context.Background(), spec)
	if err != nil || verifiedAt.IsZero() {
		t.Fatalf("real Reality Vision echo probe failed: %v", err)
	}
	if got := challenges.Load(); got != 1 {
		t.Fatalf("first probe performed %d challenges, want 1", got)
	}
	cachedAt, err := protocolprobe.Check(context.Background(), spec)
	if err != nil || !cachedAt.Equal(verifiedAt) || challenges.Load() != 1 {
		t.Fatal("unchanged rule repeated its challenge or renewed the cached timestamp", err)
	}
	spec.EvidenceKey = "isolated-rule-fragment-two"
	nextAt, err := protocolprobe.Check(context.Background(), spec)
	if err != nil || !nextAt.After(verifiedAt) || challenges.Load() != 2 {
		t.Fatal("changed rule reused old protocol evidence", err)
	}
	spec.UUID = "615db532-728a-4a34-8353-873b34d12de2"
	if _, err := protocolprobe.Check(context.Background(), spec); err == nil {
		t.Fatal("wrong VLESS identity passed the real protocol probe")
	}
}

func testSharedInventory(t *testing.T, binary string, specs []protocolprobe.Spec, challenges *atomic.Int64) {
	client := protocolprobe.NewClient(binary)
	t.Cleanup(client.Close)
	bad := specs[0]
	bad.UUID = "615db532-728a-4a34-8353-873b34d12de2"
	// Slow peers consume their deadline while healthy peers must continue.
	slowListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = slowListener.Close() })
	go func() {
		for {
			conn, err := slowListener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(time.Second))
				_, _ = io.Copy(io.Discard, conn)
			}()
		}
	}()
	var slow []protocolprobe.Spec
	for i := 0; i < 256; i++ {
		spec := specs[i]
		spec.DialPort = slowListener.Addr().(*net.TCPAddr).Port
		slow = append(slow, spec)
	}
	all := append(append(append([]protocolprobe.Spec{}, specs...), slow...), bad)
	prepareCtx, prepareCancel := context.WithTimeout(context.Background(), 10*time.Second)
	started := time.Now()
	err = client.Prepare(prepareCtx, all)
	prepareCancel() // Must not stop the persistent client's lifetime.
	if err != nil {
		t.Fatal("shared client start failed", err)
	}
	t.Logf("prepared %d distinct routes in %v", len(all), time.Since(started))
	for cycle := 0; cycle < 2; cycle++ {
		started = time.Now()
		jobs := make(chan protocolprobe.Spec)
		var wg sync.WaitGroup
		var passed, failed atomic.Int64
		for i := 0; i < protocolprobe.NetworkChecks; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for spec := range jobs {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					at, err := client.Check(ctx, spec)
					cancel()
					if err != nil || at.IsZero() {
						failed.Add(1)
					} else {
						passed.Add(1)
					}
				}
			}()
		}
		// Interleave real protocol failures instead of measuring an all-green set.
		for i, spec := range specs {
			if i%9 == 0 {
				jobs <- slow[i/9]
			}
			jobs <- spec
		}
		close(jobs)
		wg.Wait()
		if passed.Load() != int64(len(specs)) || failed.Load() != int64(len(slow)) {
			t.Fatalf("cycle %d: passed=%d failed=%d", cycle, passed.Load(), failed.Load())
		}
		if elapsed := time.Since(started); elapsed >= 45*time.Second {
			t.Fatalf("inventory renewal exceeded its 45s budget: %v", elapsed)
		} else {
			t.Logf("cycle %d: %d healthy + %d slow failed routes in %v", cycle, passed.Load(), failed.Load(), elapsed)
		}
		if challenges.Load() != int64((cycle+1)*len(specs)) {
			t.Fatal("checks did not perform fresh echo challenges")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if at, err := client.Check(ctx, bad); err == nil || !at.IsZero() {
		t.Fatal("wrong UUID borrowed another route's healthy result")
	}
	if challenges.Load() != int64(2*len(specs)) {
		t.Fatal("wrong identity reached echo service")
	}
	spec := specs[0]
	spec.EvidenceKey = "changed-fragment"
	at, err := client.Check(context.Background(), spec)
	if err != nil || at.IsZero() || challenges.Load() != int64(2*len(specs)+1) {
		t.Fatal("changed evidence did not trigger a new challenge", err)
	}
	if err := client.Prepare(context.Background(), specs[:1]); err != nil {
		t.Fatal(err)
	}
	if at, err := client.Check(context.Background(), specs[1]); err == nil || !at.IsZero() {
		t.Fatal("retired target remained authorized")
	}
	if _, err := client.Check(context.Background(), specs[0]); err != nil {
		t.Fatal("replacement client failed", err)
	}
}
