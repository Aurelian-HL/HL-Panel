package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func mixedFragments(xrayTag, gostName string) []agentv1.ConfigurationFragment {
	return []agentv1.ConfigurationFragment{
		{GroupID: "xray-rule", GroupRevision: 1, Engine: agentv1.EngineXray,
			Config: json.RawMessage(`{"inbounds":[{"tag":"` + xrayTag + `"}],"outbounds":[]}`)},
		gostFragment("gost-rule", strings.Replace(gostServiceA, "forward-a", gostName, 1)),
	}
}

func TestMixedAdapterRecoversExitedSecondEngineAfterStartFails(t *testing.T) {
	ctx := context.Background()
	xrayRunner := &recordingXrayRunner{}
	gostRunner := &recordingGOSTRunner{}
	adapter, err := NewMixedProcessAdapter(newTestXrayAdapter(t, xrayRunner, true), newTestGOSTAdapter(t, gostRunner, true))
	if err != nil {
		t.Fatal(err)
	}
	first, err := adapter.Prepare(ctx, testDesired(t, 1, mixedFragments("xray-one", "forward-one")))
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Validate(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Commit(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Verify(ctx, first); err != nil || adapter.ActiveEngineMode() != "mixed" {
		t.Fatalf("first generation was not fully active: %v, mode=%q", err, adapter.ActiveEngineMode())
	}
	second, err := adapter.Prepare(ctx, testDesired(t, 2, mixedFragments("xray-one", "forward-one")))
	if err != nil {
		t.Fatal(err)
	}
	gostRunner.startErrors = []error{errors.New("second process failed")}
	gostRunner.processes[0].setRunning(false)
	if err := adapter.Commit(ctx, second); err == nil {
		t.Fatal("second engine failure was accepted")
	}
	if mode := adapter.ActiveEngineMode(); mode != "mixed" {
		t.Fatalf("compensating rollback did not restore the previous live mode: %q", mode)
	}
	if err := adapter.Verify(ctx, first); err != nil || adapter.ActiveEngineMode() != "mixed" {
		t.Fatalf("previous complete generation not restored: %v, mode=%q", err, adapter.ActiveEngineMode())
	}
	if len(gostRunner.startErrors) != 0 || len(xrayRunner.starts) != 1 || xrayRunner.processes[0].stopCount != 0 {
		t.Fatal("start failure was not exercised or surviving Xray was restarted")
	}
}

func TestMixedAdapterValidatedCommitSkipsDuplicateValidationAndRestoresLastGood(t *testing.T) {
	ctx := context.Background()
	xrayRunner := &recordingXrayRunner{}
	gostRunner := &recordingGOSTRunner{}
	adapter, err := NewMixedProcessAdapter(newTestXrayAdapter(t, xrayRunner, true), newTestGOSTAdapter(t, gostRunner, true))
	if err != nil {
		t.Fatal(err)
	}

	first, err := adapter.Prepare(ctx, testDesired(t, 1, mixedFragments("xray-one", "forward-one")))
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Validate(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := adapter.CommitValidated(ctx, first); err != nil {
		t.Fatal(err)
	}
	if len(xrayRunner.tests) != 1 || len(gostRunner.tests) != 1 {
		t.Fatalf("first generation native validation calls: xray=%d gost=%d, want 1 each", len(xrayRunner.tests), len(gostRunner.tests))
	}
	if err := adapter.Verify(ctx, first); err != nil || adapter.ActiveEngineMode() != "mixed" {
		t.Fatalf("first generation was not fully active: %v, mode=%q", err, adapter.ActiveEngineMode())
	}

	second, err := adapter.Prepare(ctx, testDesired(t, 2, mixedFragments("xray-one", "forward-one")))
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Validate(ctx, second); err != nil {
		t.Fatal(err)
	}
	if len(xrayRunner.tests) != 2 || len(gostRunner.tests) != 2 {
		t.Fatalf("second generation validation calls: xray=%d gost=%d, want 2 each", len(xrayRunner.tests), len(gostRunner.tests))
	}
	gostRunner.startErrors = []error{errors.New("second process failed")}
	gostRunner.processes[0].setRunning(false)
	if err := adapter.CommitValidated(ctx, second); err == nil {
		t.Fatal("second engine failure was accepted")
	}
	if len(xrayRunner.tests) != 2 || len(gostRunner.tests) != 2 {
		t.Fatalf("validated commit repeated native validation: xray=%d gost=%d, want 2 each", len(xrayRunner.tests), len(gostRunner.tests))
	}
	if mode := adapter.ActiveEngineMode(); mode != "mixed" {
		t.Fatalf("compensating rollback did not restore the previous live mode: %q", mode)
	}
	if err := adapter.Verify(ctx, first); err != nil {
		t.Fatalf("previous complete generation not restored: %v", err)
	}
	if len(gostRunner.startErrors) != 0 || len(xrayRunner.starts) != 1 || xrayRunner.processes[0].stopCount != 0 {
		t.Fatal("start failure was not exercised or surviving Xray was restarted")
	}
}

func TestMixedAdapterAttestsSingleEngineOnlyWhenRunning(t *testing.T) {
	ctx := context.Background()
	adapter, err := NewMixedProcessAdapter(newTestXrayAdapter(t, &recordingXrayRunner{}, true), newTestGOSTAdapter(t, &recordingGOSTRunner{}, true))
	if err != nil {
		t.Fatal(err)
	}
	desired := testDesired(t, 1, mixedFragments("only-xray", "unused")[:1])
	prepared, err := adapter.Prepare(ctx, desired)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Commit(ctx, prepared); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Verify(ctx, prepared); err != nil || adapter.ActiveEngineMode() != "xray" {
		t.Fatalf("single Xray component was not attested: %v, mode=%q", err, adapter.ActiveEngineMode())
	}
}

func TestMixedAdapterRejectsObsoleteProcessForSingleEngineBundle(t *testing.T) {
	for _, tt := range []struct {
		name       string
		keepEngine agentv1.Engine
	}{
		{name: "xray only", keepEngine: agentv1.EngineXray},
		{name: "gost only", keepEngine: agentv1.EngineGOST},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			xray := newTestXrayAdapter(t, &recordingXrayRunner{}, true)
			gost := newTestGOSTAdapter(t, &recordingGOSTRunner{}, true)
			adapter, err := NewMixedProcessAdapter(xray, gost)
			if err != nil {
				t.Fatal(err)
			}
			fragments := mixedFragments("only-xray", "only-gost")
			if tt.keepEngine == agentv1.EngineXray {
				fragments = fragments[:1]
			} else {
				fragments = fragments[1:]
			}
			prepared, err := adapter.Prepare(ctx, testDesired(t, 1, fragments))
			if err != nil {
				t.Fatal(err)
			}
			if err := adapter.Commit(ctx, prepared); err != nil {
				t.Fatal(err)
			}
			if tt.keepEngine == agentv1.EngineXray {
				gost.process = &recordingGOSTProcess{running: true}
			} else {
				xray.process = &recordingXrayProcess{running: true}
			}
			if err := adapter.Verify(ctx, prepared); err == nil {
				t.Fatal("obsolete process was accepted")
			}
			if mode := adapter.ActiveEngineMode(); mode != "" {
				t.Fatalf("obsolete process produced valid mode %q", mode)
			}
			if err := adapter.Commit(ctx, prepared); !errors.Is(err, ErrLiveUpdateRequiresMaintenance) {
				t.Fatalf("stale prepared bundle accepted: %v", err)
			}
			if err := adapter.Rollback(ctx, &prepared); !errors.Is(err, ErrLiveUpdateRequiresMaintenance) {
				t.Fatalf("unknown running configuration was replaced: %v", err)
			}
			if !xray.HasLiveProcess() || !gost.HasLiveProcess() {
				t.Fatal("missing component caused a process stop")
			}
		})
	}
}

func TestMixedCompensationPreservesAllRollbackErrors(t *testing.T) {
	ctx := context.Background()
	xr, gr := &recordingXrayRunner{}, &recordingGOSTRunner{}
	x, g := newTestXrayAdapter(t, xr, true), newTestGOSTAdapter(t, gr, true)
	a, err := NewMixedProcessAdapter(x, g)
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Prepare(ctx, testDesired(t, 1, mixedFragments("one", "one")))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Commit(ctx, p); err != nil {
		t.Fatal(err)
	}
	// The first rollback fails validation; the second has uncertain runtime.
	// Neither the aggregate nor the compensation wrapper may discard the latter.
	xp := p.components[agentv1.EngineXray]
	if err := os.WriteFile(xp.adapterPath, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	g.liveStateUncertain = true
	cause := errors.New("second component commit failed")
	err = a.compensateCommitFailure(ctx, &p, "gost", cause)
	if !errors.Is(err, cause) || !errors.Is(err, ErrConfigurationHash) || !errors.Is(err, ErrLiveStateUncertain) {
		t.Fatalf("lost a causal error: %v", err)
	}
	if xr.processes[0].stopCount != 0 || gr.processes[0].stopCount != 0 {
		t.Fatal("compensation stopped a live process")
	}
}

func TestMixedAdapterReplaysCompleteBundleAfterRestart(t *testing.T) {
	ctx := context.Background()
	xray := newTestXrayAdapter(t, &recordingXrayRunner{}, true)
	gost := newTestGOSTAdapter(t, &recordingGOSTRunner{}, true)
	first, err := NewMixedProcessAdapter(xray, gost)
	if err != nil {
		t.Fatal(err)
	}
	desired := testDesired(t, 1, mixedFragments("restarted-xray", "restarted-gost"))
	prepared, err := first.Prepare(ctx, desired)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Commit(ctx, prepared); err != nil {
		t.Fatal(err)
	}
	if err := xray.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := gost.Close(ctx); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewMixedProcessAdapter(xray, gost)
	if err != nil {
		t.Fatal(err)
	}
	if mode := restarted.ActiveEngineMode(); mode != "" {
		t.Fatalf("fresh adapter attested without replay: %q", mode)
	}
	replayed, err := restarted.Prepare(ctx, desired)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Commit(ctx, replayed); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Verify(ctx, replayed); err != nil || restarted.ActiveEngineMode() != "mixed" {
		t.Fatalf("restarted bundle not fully active: %v, mode=%q", err, restarted.ActiveEngineMode())
	}
}
