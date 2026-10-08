package xray_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	agentconfig "github.com/hongle/hl-panel/internal/agent/config"
	agentruntime "github.com/hongle/hl-panel/internal/agent/runtime"
	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/httpapi"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/control/protocolprobe"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
)

// Uses the actual authenticated API, agent loops, engine receipts, and Reality
// challenge. Hour-long fallback intervals make a missing notification fail.
// All listeners and targets belong to this isolated loopback fixture.
func TestRuleActivationNotificationsWithRealAgentAndRealityTwice(t *testing.T) {
	binary := xrayBinary(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	certPath, keyPath, _ := testCertificate(t)
	certificate, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	decoy := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }), ErrorLog: log.New(io.Discard, "", 0)}
	go func() {
		_ = decoy.Serve(tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{certificate}}))
	}()
	t.Cleanup(func() { _ = decoy.Close() })
	socks, _ := startSOCKSLanding(t, binary)
	echoPort := freeLoopbackPort(t)
	hash, err := auth.HashPassword("disposable-loopback-administrator")
	if err != nil {
		t.Fatal(err)
	}
	store := memoryrepo.New(auth.Administrator{ID: "activation-admin", Username: "admin", PasswordHash: hash, CreatedAt: time.Now()})
	authService := auth.NewService(store, audit.NewService(store), time.Now, time.Hour)
	login, err := authService.Login(ctx, "admin", "disposable-loopback-administrator")
	if err != nil {
		t.Fatal(err)
	}
	groupService := groups.NewService(store, time.Now)
	group, err := groupService.Create(ctx, "activation-admin", "activation loopback", groups.KindEntry, endpoints.SelectionWeightedRoundRobin, "isolated")
	if err != nil {
		t.Fatal(err)
	}
	networks := groupconfig.NewService(store, time.Now)
	if _, _, err := networks.Update(ctx, "activation-admin", group.ID, groupconfig.Request{ConnectHost: "127.0.0.1", PortStart: 1024, PortEnd: 65535, AllowDirect: true, TrafficMultiplier: 1}, "loopback-network"); err != nil {
		t.Fatal(err)
	}
	identities, err := vlessidentity.NewService(store, time.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	materials, err := vlessruntime.NewService(store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	runner := gatewaymembership.NewProtocolRunner(store, binary, echoPort, time.Hour, logger)
	forward := forwarding.NewService(store, time.Now, forwarding.WithProtocolProbeEchoPort(echoPort), forwarding.WithRealityDefaults(forwarding.RealityDefaults{ServerName: "localhost", Destination: listener.Addr().String()}))
	enroll := enrollment.NewService(store, time.Now, time.Hour)
	handler := httpapi.New(authService, enroll, nodes.NewService(store, time.Now, time.Minute), groupService, endpoints.NewService(store, time.Now), generations.NewService(store, time.Now, generations.WithDeploymentNotification(runner.NotifyDeployment)), logger, httpapi.WithBusiness(customers.NewService(store, time.Now), forward, networks), httpapi.WithVLESS(identities, materials))
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	issued, err := enroll.IssueForGroup(ctx, "activation-admin", "activation-node", group.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HL_ACTIVATION_TEST_TOKEN", issued.Token)
	root := t.TempDir()
	cfgPath := filepath.Join(root, "agent.json")
	raw, _ := json.Marshal(map[string]any{"control_plane_url": server.URL, "allow_insecure_loopback": true, "data_dir": filepath.Join(root, "data"), "hostname": "activation-node", "dial_host": "127.0.0.1", "enrollment_token_env": "HL_ACTIVATION_TEST_TOKEN", "engine_mode": "xray", "xray_binary_path": binary, "xray_auto_start": true, "heartbeat_interval": "1h", "desired_poll_interval": "1h", "request_timeout": "15s", "protocol_probe_echo_port": echoPort, "xray_api_address": localAddress(freeLoopbackPort(t))})
	if err := os.WriteFile(cfgPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := agentconfig.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := agentruntime.New(cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if err := agent.HeartbeatOnce(ctx); err != nil {
		t.Fatal(err)
	}
	doneAgent, doneRunner := make(chan error, 1), make(chan error, 1)
	go func() { doneAgent <- agent.Run(ctx) }()
	go func() { doneRunner <- runner.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		for _, done := range []chan error{doneAgent, doneRunner} {
			select {
			case <-done:
			case <-time.After(15 * time.Second):
				t.Error("activation fixture did not stop")
			}
		}
		_ = agent.Close(context.Background())
	})
	for round := 1; round <= 2; round++ {
		started := time.Now()
		request := forwarding.Request{Name: fmt.Sprintf("activation-%d", round), EntryGroupID: group.ID, EgressMode: forwarding.EgressDirect, Protocol: forwarding.ProtocolTCP, IngressProtocol: forwarding.IngressVLESSReality, ListenPort: freeLoopbackPort(t), SelectionPolicy: forwarding.SelectionRoundRobin, VLESSOutboundMode: forwarding.VLESSOutboundSOCKS5, VLESSSOCKS5Host: socks.SOCKS5.Hostname, VLESSSOCKS5Port: socks.SOCKS5.Port, VLESSSOCKS5Username: socks.SOCKS5.Username, VLESSSOCKS5Password: socks.SOCKS5.Password}
		encoded, _ := json.Marshal(request)
		httpRequest, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/api/v1/forwarding-rules", bytes.NewReader(encoded))
		httpRequest.Header.Set("Authorization", "Bearer "+login.AccessToken)
		httpRequest.Header.Set("Content-Type", "application/json")
		httpRequest.Header.Set("Idempotency-Key", fmt.Sprintf("activation-%d", round))
		response, err := server.Client().Do(httpRequest)
		if err != nil {
			t.Fatal(err)
		}
		var created struct {
			Rule forwarding.Rule `json:"rule"`
		}
		err = json.NewDecoder(response.Body).Decode(&created)
		response.Body.Close()
		if response.StatusCode != 201 || err != nil {
			t.Fatalf("save rule returned %d: %v", response.StatusCode, err)
		}
		deadline := started.Add(12 * time.Second)
		for {
			current, err := forward.GetForAdministrator(ctx, "activation-admin", created.Rule.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.Status == forwarding.StatusActive {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("round %d still pending with notifications; reason=%s", round, current.ActivationReason)
			}
			time.Sleep(25 * time.Millisecond)
		}
		credential, err := identities.CredentialForAdministrator(ctx, "activation-admin", created.Rule.ID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = protocolprobe.Check(ctx, protocolprobe.Spec{XrayBinary: binary, DialHost: "127.0.0.1", DialPort: created.Rule.ListenPort, UUID: credential.CredentialUUID, ServerName: created.Rule.RealityServerName, PublicKey: created.Rule.RealityPublicKey, ShortID: created.Rule.RealityShortID, EchoHost: "127.0.0.1", EchoPort: echoPort})
		if err != nil {
			t.Fatalf("round %d actual customer forwarding failed: %v", round, err)
		}
		probe, err := store.ProtocolProbeConfig(ctx, created.Rule.ID)
		if err != nil || probe.UUID == credential.CredentialUUID {
			t.Fatal("probe and customer identities were not independent")
		}
		t.Logf("round %d saved, notified, engine verified, protocol activated, and customer transferred in %s", round, time.Since(started))
	}
}
