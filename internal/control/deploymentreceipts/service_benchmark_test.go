package deploymentreceipts

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// Each list pass evaluates every rule against the same complete node bundle.
func BenchmarkEvaluateRuleList(b *testing.B) {
	for _, count := range []int{100, 300} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			input := completeInput(b, agentv1.EngineXray)
			fragments := make([]agentv1.ConfigurationFragment, count)
			for i := range fragments {
				fragments[i] = agentv1.ConfigurationFragment{
					GroupID:       generations.ForwardingFragmentID(agentv1.EngineXray, fmt.Sprint(i)),
					GroupRevision: 3, Engine: agentv1.EngineXray,
					Config: json.RawMessage(`{"inbounds":[],"padding":"` + strings.Repeat("x", 1024) + `"}`),
				}
			}
			setBundle(b, &input, fragments)
			for phase, result := range input.ApplyResults {
				result.ConfigSHA256 = input.CurrentConfig.ConfigSHA256
				input.ApplyResults[phase] = result
			}
			inputs := make([]Input, count)
			for i := range inputs {
				inputs[i] = input
				inputs[i].Rule.ID = fmt.Sprint(i)
				if !Evaluate(inputs[i]).ReceiptVerified {
					b.Fatal("invalid benchmark fixture")
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for _, item := range inputs {
					if !Evaluate(item).ReceiptVerified {
						b.Fatal("receipt rejected")
					}
				}
			}
		})
	}
}
