package engine

import (
	"context"
	"encoding/json"
	"errors"
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

func TestMixedAdapterRestoresBothEnginesAfterSecondStartFails(t *testing.T) {
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
	second, err := adapter.Prepare(ctx, testDesired(t, 2, mixedFragments("xray-two", "forward-two")))
	if err != nil {
		t.Fatal(err)
	}
	gostRunner.startErrors = []error{errors.New("second process failed")}
	if err := adapter.Commit(ctx, second); err == nil {
		t.Fatal("second engine failure was accepted")
	}
	if mode := adapter.ActiveEngineMode(); mode != "" {
		t.Fatalf("partial activation reported live mode %q", mode)
	}
	if err := adapter.Rollback(ctx, &first); err != nil {
		t.Fatalf("restore previous complete generation: %v", err)
	}
	if err := adapter.Verify(ctx, first); err != nil || adapter.ActiveEngineMode() != "mixed" {
		t.Fatalf("previous complete generation not restored: %v, mode=%q", err, adapter.ActiveEngineMode())
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
			if err := adapter.Rollback(ctx, &prepared); err != nil {
				t.Fatal(err)
			}
			if err := adapter.Verify(ctx, prepared); err != nil {
				t.Fatalf("single-engine bundle not restored: %v", err)
			}
		})
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
