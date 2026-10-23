package xray_test

import (
	"crypto/rand"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/engine"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/forwarding/vlessconfig"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	"github.com/hongle/hl-panel/internal/provisioning/vless"
	"golang.org/x/net/proxy"
)

// Use unchanged production compiler output and a real Reality client. Both
// already established streams and new handshakes must survive egress edits.
// The client processes and subscription URIs never change during this test.
func TestProductionRealityEgressEditsPreserveConnections(t *testing.T) {
	for _, failure := range []string{"", "ado", "adrules", "rmo"} {
		name := failure
		if name == "" {
			name = "success"
		}
		t.Run(name, func(t *testing.T) {
			f := newLiveUpdateFixture(t)
			publicKey, privateKey, err := vless.GenerateRealityKeyPair(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			cert, key, _ := testCertificate(t)
			certificate, err := tls.LoadX509KeyPair(cert, key)
			if err != nil {
				t.Fatal(err)
			}
			decoyListener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			decoy := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }), ErrorLog: log.New(io.Discard, "", 0)}
			go func() {
				_ = decoy.Serve(tls.NewListener(decoyListener, &tls.Config{Certificates: []tls.Certificate{certificate}}))
			}()
			t.Cleanup(func() { _ = decoy.Close() })
			profile := vlessconfig.RealityProfile{ServerName: "localhost", Dest: decoyListener.Addr().String(), PrivateKey: privateKey, ShortIDs: []string{"01234567"}, Flow: "xtls-rprx-vision"}
			identities := []string{"04f78a4e-bb88-4a65-86f0-9b7c53f11e0d", "615db532-728a-4a34-8353-873b34d12de2"}
			prepare := func(target int) engine.PreparedConfiguration {
				t.Helper()
				f.generation++
				var fragments []agentv1.ConfigurationFragment
				for i := range identities {
					targetIndex := 0
					if i == 0 {
						targetIndex = target
					}
					rule := forwarding.Rule{ID: fmt.Sprintf("continuity-%d", i), IngressProtocol: forwarding.IngressVLESSReality, Protocol: forwarding.ProtocolTCP, EgressMode: forwarding.EgressDirect, Targets: []forwarding.Target{{Host: "127.0.0.1", Port: f.targets[targetIndex]}}}
					raw, err := vlessconfig.CompileDirect(rule, vlessconfig.Identity{UUID: identities[i]}, profile, vlessconfig.Listener{Address: "127.0.0.1", Port: f.ports[i]})
					if err != nil {
						t.Fatal(err)
					}
					fragments = append(fragments, agentv1.ConfigurationFragment{GroupID: rule.ID, GroupRevision: 1, Engine: agentv1.EngineXray, Config: raw})
				}
				p, err := f.a.Prepare(f.ctx, adapterDesired(t, f.generation, fragments))
				if err != nil {
					t.Fatal(err)
				}
				return p
			}
			initial := prepare(0)
			if err := f.a.Commit(f.ctx, initial); err != nil {
				t.Fatal(err)
			}
			if err := f.a.Verify(f.ctx, initial); err != nil {
				t.Fatal(err)
			}
			var dialers []proxy.Dialer
			for i, identity := range identities {
				uri, err := vless.URI(vless.Profile{
					Endpoint: vless.Endpoint{Hostname: "127.0.0.1", Port: f.ports[i], Name: "continuity"}, Identity: vless.Identity{UUID: identity, Flow: profile.Flow},
					Reality: &vless.RealityProfile{ServerName: profile.ServerName, Destination: profile.Dest, PublicKey: publicKey, ShortID: profile.ShortIDs[0], Fingerprint: "chrome", SpiderX: "/"},
				})
				if err != nil {
					t.Fatal(err)
				}
				port := freeLoopbackPort(t)
				startXray(t, xrayBinary(t), unmodifiedProductionConfig(t, realityNativeClientFromURI(t, uri, port)), localAddress(port))
				dialer, err := proxy.SOCKS5("tcp", localAddress(port), nil, &net.Dialer{Timeout: time.Second})
				if err != nil {
					t.Fatal(err)
				}
				dialers = append(dialers, dialer)
				c, err := dialer.Dial("tcp", localAddress(f.targets[0]))
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				if err := exchangeUpdate(c, 'A'); err != nil {
					t.Fatal(err)
				}
				defer f.monitor(c)()
			}
			fresh := func(dialer proxy.Dialer) error {
				c, err := dialer.Dial("tcp", localAddress(f.targets[0]))
				if err != nil {
					return err
				}
				defer c.Close()
				return freshUpdateReply(c)
			}
			var checkpoints, sessions atomic.Int64
			f.runner.after = func(operation string) {
				if operation == "lsi" || operation == "lso" {
					return
				}
				checkpoints.Add(1)
				for i, dialer := range dialers {
					if err := fresh(dialer); err != nil {
						t.Errorf("Reality fresh connection rule %d after %s: %v", i, operation, err)
					}
				}
			}
			stop, done := make(chan struct{}), make(chan error, 1)
			go func() {
				for {
					select {
					case <-stop:
						done <- nil
						return
					default:
					}
					if err := fresh(dialers[0]); err != nil {
						done <- err
						return
					}
					sessions.Add(1)
					time.Sleep(time.Millisecond)
				}
			}()
			defer func() {
				close(stop)
				if err := <-done; err != nil {
					t.Error("Reality new connection interrupted", err)
				}
				if checkpoints.Load() == 0 || sessions.Load() == 0 {
					t.Error("Reality continuity did not exercise changes")
				}
				t.Logf("Reality RPC checkpoints=%d, concurrent new sessions=%d", checkpoints.Load(), sessions.Load())
			}()
			for i := 0; i < 4; i++ {
				target := 1 - i%2
				if failure != "" {
					target = 1
				}
				candidate := prepare(target)
				f.runner.fail = failure
				err := f.a.Commit(f.ctx, candidate)
				if failure != "" {
					// Always modify A -> B when exercising failure and compensation.
					if err == nil || f.runner.fail != "" {
						t.Fatalf("Reality fault not exercised: %v", err)
					}
					if err := f.a.Verify(f.ctx, initial); err != nil {
						t.Fatal("Reality old generation not restored", err)
					}
					continue
				} else if err != nil {
					t.Fatal(err)
				}
				if err := f.a.Verify(f.ctx, candidate); err != nil {
					t.Fatal(err)
				}
				c, err := dialers[0].Dial("tcp", localAddress(f.targets[0]))
				if err != nil {
					t.Fatal(err)
				}
				err = exchangeUpdate(c, byte('A'+target))
				_ = c.Close()
				if err != nil {
					t.Fatal("Reality new destination", err)
				}
			}
		})
	}
}
