package gost_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/engine"
	"github.com/hongle/hl-panel/internal/agent/reconciler"
	"github.com/hongle/hl-panel/internal/agent/state"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// Keep one actual TCP session open through updates and rollbacks of the other
// engine. A fresh dial after each update would hide an unnecessary restart.
func TestMixedEngineUpdatePreservesExistingOtherEngineConnection(t *testing.T) {
	gostBinary := verifiedGOST(t)
	xrayBinary := os.Getenv("NYVP_XRAY_BINARY")
	if xrayBinary == "" {
		t.Skip("set NYVP_XRAY_BINARY as well to exercise both real engines")
	}
	if !filepath.IsAbs(xrayBinary) {
		t.Fatal("NYVP_XRAY_BINARY must be absolute")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	version, err := exec.CommandContext(ctx, xrayBinary, "version").Output()
	if err != nil || !strings.HasPrefix(string(version), "Xray 26.7.28 ") {
		t.Fatal("mixed engine integration requires pinned Xray 26.7.28")
	}
	for _, changed := range []string{"xray", "gost"} {
		t.Run(changed, func(t *testing.T) {
			echo, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			var handlers sync.WaitGroup
			var connectionMu sync.Mutex
			connections := make(map[net.Conn]struct{})
			closing := false
			handlers.Add(1)
			go func() {
				defer handlers.Done()
				for {
					c, err := echo.Accept()
					if err != nil {
						return
					}
					connectionMu.Lock()
					if closing {
						connectionMu.Unlock()
						_ = c.Close()
						return
					}
					connections[c] = struct{}{}
					connectionMu.Unlock()
					handlers.Add(1)
					go func() {
						defer handlers.Done()
						defer c.Close()
						defer func() {
							connectionMu.Lock()
							delete(connections, c)
							connectionMu.Unlock()
						}()
						_ = c.SetDeadline(time.Now().Add(15 * time.Second))
						_, _ = io.Copy(c, c)
					}()
				}
			}()
			t.Cleanup(func() {
				_ = echo.Close()
				connectionMu.Lock()
				closing = true
				for c := range connections {
					_ = c.Close()
				}
				connectionMu.Unlock()
				handlers.Wait()
			})
			targetPort := echo.Addr().(*net.TCPAddr).Port
			xrayPort, gostPort := unusedLoopbackPort(t), unusedLoopbackPort(t)
			root := t.TempDir()
			xray, err := engine.NewXrayProcessAdapter(engine.XrayAdapterOptions{
				StagingDir: filepath.Join(root, "xray-staging"), ActivePath: filepath.Join(root, "xray.json"),
				BinaryPath: xrayBinary, AutoStart: true,
				StatsAPIAddress: fmt.Sprintf("127.0.0.1:%d", unusedLoopbackPort(t)),
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = xray.Close(context.Background()) })
			gost, err := engine.NewGOSTProcessAdapter(engine.GOSTAdapterOptions{
				StagingDir: filepath.Join(root, "gost-staging"), ActivePath: filepath.Join(root, "gost.json"),
				BinaryPath: gostBinary, AutoStart: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = gost.Close(context.Background()) })
			adapter, err := engine.NewMixedProcessAdapter(xray, gost)
			if err != nil {
				t.Fatal(err)
			}
			prepare := func(generation agentv1.NodeConfigGeneration, xrayName, gostName string) engine.PreparedConfiguration {
				t.Helper()
				fragments := []agentv1.ConfigurationFragment{
					{GroupID: "xray", GroupRevision: 1, Engine: agentv1.EngineXray,
						Config: json.RawMessage(fmt.Sprintf(`{"log":{"loglevel":"none"},"inbounds":[{"tag":%q,"listen":"127.0.0.1","port":%d,"protocol":"dokodemo-door","settings":{"address":"127.0.0.1","port":%d,"network":"tcp"}}],"outbounds":[{"protocol":"freedom","settings":{"finalRules":[{"network":"tcp","ip":["127.0.0.1/32"],"port":"%d","action":"allow"}]}}]}`, xrayName, xrayPort, targetPort, targetPort))},
					{GroupID: "gost", GroupRevision: 1, Engine: agentv1.EngineGOST,
						Config: json.RawMessage(fmt.Sprintf(`{"services":[{"name":%q,"addr":"127.0.0.1:%d","handler":{"type":"tcp"},"listener":{"type":"tcp"},"metadata":{"enableStats":true},"forwarder":{"nodes":[{"name":"echo","addr":"127.0.0.1:%d"}],"selector":{"strategy":"round","maxFails":1,"failTimeout":30000000000}}}]}`, gostName, gostPort, targetPort))},
				}
				data, err := json.Marshal(agentv1.ConfigurationBundle{SchemaVersion: 1, Fragments: fragments})
				if err != nil {
					t.Fatal(err)
				}
				hash := sha256.Sum256(data)
				p, err := adapter.Prepare(ctx, agentv1.DesiredNodeConfig{Generation: generation, Engine: agentv1.EngineNodeBundle, Config: data, ConfigSHA256: hex.EncodeToString(hash[:])})
				if err != nil {
					t.Fatal(err)
				}
				return p
			}
			first := prepare(1, "xray-one", "gost-one")
			if err := adapter.Commit(ctx, first); err != nil {
				t.Fatal(err)
			}
			port, epoch := gostPort, gost.CounterEpoch
			if changed == "gost" {
				port, epoch = xrayPort, xray.CounterEpoch
			}
			initialEpoch := epoch()
			var connection net.Conn
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				connection, err = net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
				if err == nil {
					break
				}
				time.Sleep(25 * time.Millisecond)
			}
			if err != nil || connection == nil {
				t.Fatal("unchanged engine listener did not become ready")
			}
			defer connection.Close()
			checkStream := func() {
				t.Helper()
				_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
				payload := "existing-session-survives"
				if _, err := io.WriteString(connection, payload); err != nil {
					t.Fatalf("existing session write failed: %v", err)
				}
				response := make([]byte, len(payload))
				if _, err := io.ReadFull(connection, response); err != nil || string(response) != payload {
					t.Fatalf("existing session was interrupted: %v", err)
				}
				if epoch() != initialEpoch {
					t.Fatal("unchanged engine counter epoch changed")
				}
			}
			checkStream()
			for i := 2; i <= 4; i++ {
				xrayName, gostName := "xray-one", "gost-one"
				if changed == "xray" {
					xrayName = fmt.Sprintf("xray-%d", i)
				} else {
					gostName = fmt.Sprintf("gost-%d", i)
				}
				p := prepare(agentv1.NodeConfigGeneration(i), xrayName, gostName)
				if err := adapter.Commit(ctx, p); err != nil {
					t.Fatal(err)
				}
				if err := adapter.Verify(ctx, p); err != nil {
					t.Fatal(err)
				}
				checkStream()
				if err := adapter.Rollback(ctx, &first); err != nil {
					t.Fatal(err)
				}
				checkStream()
			}
			// The changed engine exits; a temporary recovery staging/validation
			// error must leave the other engine and this exact TCP session alive.
			store := state.NewAtomicStateStore(filepath.Join(root, "state.json"), filepath.Join(root, "configurations"))
			desired := first.DesiredNodeConfig()
			if _, err := store.Begin(desired, time.Now()); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Complete(desired, time.Now()); err != nil {
				t.Fatal(err)
			}
			if changed == "xray" {
				err = xray.Close(ctx)
			} else {
				err = gost.Close(ctx)
			}
			if err != nil || adapter.ActiveEngineMode() != "" {
				t.Fatalf("partial failure not established: %v", err)
			}
			fault := &recoveryFaultAdapter{MixedProcessAdapter: adapter}
			r, err := reconciler.New(store, fault, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			for _, phase := range []agentv1.ApplyPhase{agentv1.ApplyPhasePrepare, agentv1.ApplyPhaseValidate} {
				fault.phase = phase
				if attempted, err := r.EnsureApplied(ctx); !attempted || err == nil {
					t.Fatalf("%s failure was not exercised: %v, %v", phase, attempted, err)
				}
				checkStream()
			}
			fault.phase = ""
			if attempted, err := r.EnsureApplied(ctx); !attempted || err != nil {
				t.Fatalf("retry did not recover missing engine: %v, %v", attempted, err)
			}
			if err := adapter.Verify(ctx, first); err != nil {
				t.Fatal(err)
			}
			checkStream()
		})
	}
}

type recoveryFaultAdapter struct {
	*engine.MixedProcessAdapter
	phase agentv1.ApplyPhase
}

func (a *recoveryFaultAdapter) Prepare(ctx context.Context, desired agentv1.DesiredNodeConfig) (engine.PreparedConfiguration, error) {
	if a.phase == agentv1.ApplyPhasePrepare {
		return engine.PreparedConfiguration{}, errors.New("injected staging failure")
	}
	return a.MixedProcessAdapter.Prepare(ctx, desired)
}

func (a *recoveryFaultAdapter) Validate(ctx context.Context, prepared engine.PreparedConfiguration) error {
	if a.phase == agentv1.ApplyPhaseValidate {
		return errors.New("injected validation failure")
	}
	return a.MixedProcessAdapter.Validate(ctx, prepared)
}
