package runtime

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/controlclient"
	"github.com/hongle/hl-panel/internal/agent/engine"
	"github.com/hongle/hl-panel/internal/agent/reconciler"
	"github.com/hongle/hl-panel/internal/agent/state"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestNodeUpdateVerificationAfterDesiredIsAlreadyApplied(t *testing.T) {
	for _, scenario := range []string{"already_applied", "new_desired", "pending", "corrupt_saved_bundle", "missing_saved_bundle", "invalid_active_config", "panel_unavailable", "fresh_node"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			store := state.NewAtomicStateStore(filepath.Join(dir, "state.json"), filepath.Join(dir, "configs"))
			adapter, err := engine.NewDryRunFileAdapter(filepath.Join(dir, "staged.json"), filepath.Join(dir, "active.json"))
			if err != nil {
				t.Fatal(err)
			}
			reconcile, err := reconciler.New(store, adapter, nil, "test-credential")
			if err != nil {
				t.Fatal(err)
			}
			raw := []byte(`{"schema_version":1,"fragments":[]}`)
			desired := agentv1.DesiredNodeConfig{Generation: 1, Engine: agentv1.EngineNodeBundle, Config: raw, ConfigSHA256: generations.SHA256Hex(raw)}
			if scenario != "fresh_node" {
				if err := reconcile.Apply(ctx, desired); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "pending":
				next := desired
				next.Generation++
				if _, err := store.Begin(next, time.Now()); err != nil {
					t.Fatal(err)
				}
			case "corrupt_saved_bundle", "missing_saved_bundle":
				files, err := filepath.Glob(filepath.Join(dir, "configs", "*.json"))
				if err != nil || len(files) != 1 {
					t.Fatal("saved bundle missing")
				}
				if scenario == "missing_saved_bundle" {
					err = os.Remove(files[0])
				} else {
					err = os.WriteFile(files[0], []byte(`{}`), 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "invalid_active_config":
				if err := os.WriteFile(filepath.Join(dir, "active.json"), []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/agent/desired" || r.Header.Get("Authorization") != "Bearer test-credential" {
					t.Error("unexpected request")
					w.WriteHeader(400)
					return
				}
				if scenario == "panel_unavailable" {
					w.WriteHeader(503)
					return
				}
				if scenario == "new_desired" {
					next := desired
					next.Generation++
					_ = json.NewEncoder(w).Encode(next)
					return
				}
				w.WriteHeader(204)
			}))
			defer server.Close()
			client, err := controlclient.NewWithOptions(server.URL, server.Client(), true)
			if err != nil {
				t.Fatal(err)
			}
			a := &Agent{state: store, adapter: adapter, client: client, nodeSecret: "test-credential"}
			generation, hash, err := a.verifyNodeUpdate(ctx)
			if scenario == "fresh_node" {
				if err != nil || generation != 0 || hash != "" {
					t.Fatalf("fresh verification: %d %q %v", generation, hash, err)
				}
			} else if scenario == "already_applied" {
				if err != nil || generation != 1 || hash != desired.ConfigSHA256 {
					t.Fatalf("applied verification: %d %q %v", generation, hash, err)
				}
			} else if err == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
}

func TestUpdateVerificationRequiresOwnedTCPAndUDPListeners(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("Linux proc socket ownership")
	}
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	config, _ := json.Marshal(map[string]any{"services": []any{map[string]any{"addr": tcp.Addr().String(), "listener": map[string]string{"type": "tcp"}}, map[string]any{"addr": udp.LocalAddr().String(), "listener": map[string]string{"type": "udp"}}}})
	f := agentv1.ConfigurationFragment{Engine: agentv1.EngineGOST, Config: config}
	if err := verifyFragmentSockets(os.Getpid(), f); err != nil {
		t.Fatal(err)
	}
	if err := verifyFragmentSockets(1, f); err == nil {
		t.Fatal("unowned listeners accepted")
	}
	udp.Close()
	if err := verifyFragmentSockets(os.Getpid(), f); err == nil {
		t.Fatal("missing UDP listener accepted")
	}
}
