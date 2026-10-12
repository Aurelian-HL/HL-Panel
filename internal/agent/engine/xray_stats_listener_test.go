package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestXrayAdapterStatsAddressSurvivesLifecycle(t *testing.T) {
	t.Parallel()
	for _, address := range []string{"127.0.0.1:19473", "[::1]:19474", "localhost:19475"} {
		t.Run(address, func(t *testing.T) {
			root := t.TempDir()
			a, err := NewXrayProcessAdapter(XrayAdapterOptions{
				StagingDir: filepath.Join(root, "staging"), ActivePath: filepath.Join(root, "active.json"),
				Runner: &recordingXrayRunner{}, AutoStart: true, StatsAPIAddress: address,
			})
			if err != nil {
				t.Fatal(err)
			}
			desired := xrayDesired(t, 1, []agentv1.ConfigurationFragment{{
				GroupID: "local", GroupRevision: 1, Engine: agentv1.EngineXray,
				Config: json.RawMessage(`{"inbounds":[],"outbounds":[]}`),
			}})
			ctx := context.Background()
			prepared, err := a.Prepare(ctx, desired)
			if err != nil {
				t.Fatal(err)
			}
			if err := a.Validate(ctx, prepared); err != nil {
				t.Fatal(err)
			}
			if err := a.Commit(ctx, prepared); err != nil {
				t.Fatal(err)
			}
			if err := a.Verify(ctx, prepared); err != nil {
				t.Fatal(err)
			}
			if err := a.Rollback(ctx, &prepared); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(a.activePath)
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				Inbounds []struct {
					Listen string
					Port   int
				}
			}
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatal(err)
			}
			host, port, _ := xrayStatsListener(address)
			if len(doc.Inbounds) != 1 || doc.Inbounds[0].Listen != host || doc.Inbounds[0].Port != port {
				t.Fatalf("stats address ignored: %#v", doc.Inbounds)
			}
		})
	}
}

func TestXrayAdapterRejectsUnsafeStatsAddress(t *testing.T) {
	t.Parallel()
	for _, address := range []string{"0.0.0.0:10085", "192.0.2.1:10085", "[::]:10085", "example.com:10085", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:http", "127.0.0.1:-1"} {
		t.Run(address, func(t *testing.T) {
			root := t.TempDir()
			_, err := NewXrayProcessAdapter(XrayAdapterOptions{
				StagingDir: filepath.Join(root, "staging"), ActivePath: filepath.Join(root, "active.json"),
				Runner: &recordingXrayRunner{}, StatsAPIAddress: address,
			})
			if err == nil {
				t.Fatal("unsafe statistics listener accepted")
			}
		})
	}
}
