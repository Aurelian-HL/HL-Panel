package deploymentreceipts

import (
	"reflect"
	"testing"

	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestImmutableEvaluatorRechecksLiveEvidence(t *testing.T) {
	input := completeInput(t, agentv1.EngineXray)
	e := NewEvaluator([]generations.NodeConfigGeneration{*input.CurrentConfig})
	cases := map[string]func(*Input){
		"unchanged":         func(*Input) {},
		"retired":           func(i *Input) { i.ActiveMember = false },
		"invalidated":       func(i *Input) { i.AttemptInvalidated = true },
		"new attempt":       func(i *Input) { i.CurrentAttemptID = "different" },
		"new generation":    func(i *Input) { i.Node.DesiredGeneration++ },
		"new rule revision": func(i *Input) { i.Rule.Revision++ },
		"paused":            func(i *Input) { i.Rule.Paused = true },
		"missing receipt":   func(i *Input) { i.ApplyResults = nil },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			local := input
			change(&local)
			if got, want := e.Evaluate(local), Evaluate(local); !reflect.DeepEqual(got, want) {
				t.Fatalf("evidence changed: got %+v want %+v", got, want)
			}
		})
	}
}

func TestImmutableEvaluatorRetainsAppliedFallbackAndRejectsChangedEvidence(t *testing.T) {
	input := pendingUnrelatedBundle(t)
	e := NewEvaluator([]generations.NodeConfigGeneration{*input.CurrentConfig, *input.AppliedConfig})
	if got, want := e.Evaluate(input), Evaluate(input); !got.ReceiptVerified || !reflect.DeepEqual(got, want) {
		t.Fatalf("applied fallback changed: got %+v want %+v", got, want)
	}
	input.AppliedInvalidated = true
	if got, want := e.Evaluate(input), Evaluate(input); got.ReceiptVerified || !reflect.DeepEqual(got, want) {
		t.Fatalf("invalidated fallback survived: got %+v want %+v", got, want)
	}
}

func TestImmutableEvaluatorRejectsCorruptBundleAtConstruction(t *testing.T) {
	input := completeInput(t, agentv1.EngineXray)
	input.CurrentConfig.Config = append([]byte(nil), input.CurrentConfig.Config...)
	input.CurrentConfig.Config[0] ^= 1
	e := NewEvaluator([]generations.NodeConfigGeneration{*input.CurrentConfig})
	if got := e.Evaluate(input); got.Reason != ReasonConfigInvalid || got.ReceiptVerified {
		t.Fatalf("corrupt config accepted: %+v", got)
	}
}
