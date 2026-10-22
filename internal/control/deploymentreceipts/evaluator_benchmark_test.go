package deploymentreceipts

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// Model an immutable read view with independent bundles on several nodes. Warm
// the shared structural cache so this measures repeated publication and use,
// not initial parsing or fixture creation.
func BenchmarkEvaluatorPublication(b *testing.B) {
	configs := make([]generations.NodeConfigGeneration, 24)
	for n := range configs {
		fragments := make([]agentv1.ConfigurationFragment, 100)
		for i := range fragments {
			fragments[i] = agentv1.ConfigurationFragment{
				GroupID: fmt.Sprintf("forwarding-vless/node-%d-rule-%d", n, i), GroupRevision: 1,
				Engine: agentv1.EngineXray, Config: json.RawMessage(`{"padding":"` + strings.Repeat("x", 1024) + `"}`),
			}
		}
		raw, err := json.Marshal(agentv1.ConfigurationBundle{SchemaVersion: 1, Fragments: fragments})
		if err != nil {
			b.Fatal(err)
		}
		configs[n] = generations.NodeConfigGeneration{NodeID: fmt.Sprintf("node-%d", n), Generation: 1,
			Engine: agentv1.EngineNodeBundle, Config: raw, ConfigSHA256: generations.SHA256Hex(raw)}
		if _, ok := (*Evaluator)(nil).bundle(&configs[n]); !ok {
			b.Fatal("invalid benchmark configuration")
		}
	}
	for _, used := range []int{0, 1, len(configs)} {
		b.Run(fmt.Sprintf("nodes_24_used_%d", used), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				e := NewEvaluator(configs)
				for j := 0; j < used; j++ {
					if _, ok := e.bundle(&configs[j]); !ok {
						b.Fatal("configuration not verified")
					}
				}
			}
		})
	}
}
