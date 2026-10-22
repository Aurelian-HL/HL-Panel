package deploymentreceipts

import (
	"reflect"
	"sync"
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

func TestImmutableEvaluatorDefersUnrequestedBundlesAndRechecksHash(t *testing.T) {
	input := completeInput(t, agentv1.EngineXray)
	// Populate the shared structural cache first. A later view must still
	// verify its own bytes before using the existing index for that hash.
	if !Evaluate(input).ReceiptVerified {
		t.Fatal("fixture is not verified")
	}
	other := *input.CurrentConfig
	other.NodeID = "other-node"
	other.Config = append([]byte(nil), other.Config...)
	other.Config[0] ^= 1
	e := NewEvaluator([]generations.NodeConfigGeneration{*input.CurrentConfig, other})
	key := evidenceKey{other.NodeID, other.Generation, other.ConfigSHA256}
	if e.bundles[key].raw == nil || e.bundles[key].index != nil {
		t.Fatal("construction eagerly processed an unrelated bundle")
	}
	if !e.Evaluate(input).ReceiptVerified || e.bundles[key].raw == nil {
		t.Fatal("one-node evaluation processed another node")
	}
	if _, ok := e.bundle(&other); ok || e.bundles[key].raw != nil {
		t.Fatal("corrupt bytes reused a cached valid index or retained raw evidence")
	}
	unknown := *input.CurrentConfig
	unknown.Generation++
	if _, ok := e.bundle(&unknown); ok {
		t.Fatal("unknown generation accepted")
	}
}

func TestImmutableEvaluatorConcurrentFirstUse(t *testing.T) {
	input := completeInput(t, agentv1.EngineXray)
	e := NewEvaluator([]generations.NodeConfigGeneration{*input.CurrentConfig})
	want := Evaluate(input)
	var workers sync.WaitGroup
	for i := 0; i < 32; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 10; j++ {
				if got := e.Evaluate(input); !reflect.DeepEqual(got, want) {
					t.Errorf("concurrent evidence changed: got %+v want %+v", got, want)
					return
				}
			}
		}()
	}
	workers.Wait()
}

func TestImmutableEvaluatorDuplicateKeyRetainsValidEvidence(t *testing.T) {
	input := completeInput(t, agentv1.EngineXray)
	good := *input.CurrentConfig
	bad := good
	bad.Config = append([]byte(nil), good.Config...)
	bad.Config[0] ^= 1
	for _, configs := range [][]generations.NodeConfigGeneration{{good, bad}, {bad, good}} {
		if got := NewEvaluator(configs).Evaluate(input); !got.ReceiptVerified {
			t.Fatalf("invalid duplicate displaced valid evidence: %+v", got)
		}
	}
}
