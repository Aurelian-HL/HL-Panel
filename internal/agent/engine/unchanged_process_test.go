package engine

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// Updating one component must not terminate the other engine's sessions,
// including when the changed component has to be rolled back.
func TestMixedAdapterRejectsUnsupportedUpdateWithoutTouchingEitherEngine(t *testing.T) {
	for _, changed := range []agentv1.Engine{agentv1.EngineXray, agentv1.EngineGOST} {
		t.Run(string(changed), func(t *testing.T) {
			ctx := context.Background()
			xrayRunner, gostRunner := &recordingXrayRunner{}, &recordingGOSTRunner{}
			xray := newTestXrayAdapter(t, xrayRunner, true)
			gost := newTestGOSTAdapter(t, gostRunner, true)
			adapter, err := NewMixedProcessAdapter(xray, gost)
			if err != nil {
				t.Fatal(err)
			}
			first, err := adapter.Prepare(ctx, testDesired(t, 1, mixedFragments("xray-one", "gost-one")))
			if err != nil {
				t.Fatal(err)
			}
			if err := adapter.Commit(ctx, first); err != nil {
				t.Fatal(err)
			}
			xrayProcess, gostProcess := xray.process, gost.process
			xrayEpoch, gostEpoch := xray.CounterEpoch(), gost.CounterEpoch()
			xrayTag, gostName := "xray-one", "gost-one"
			if changed == agentv1.EngineXray {
				xrayTag = "xray-two"
			} else {
				gostName = "gost-two"
			}
			second, err := adapter.Prepare(ctx, testDesired(t, 2, mixedFragments(xrayTag, gostName)))
			if err != nil {
				t.Fatal(err)
			}
			assertUnchanged := func() {
				t.Helper()
				if changed == agentv1.EngineGOST {
					if xray.process != xrayProcess || !xrayProcess.Running() || xray.CounterEpoch() != xrayEpoch {
						t.Fatal("GOST update interrupted unchanged Xray process or reset counters")
					}
				} else if gost.process != gostProcess || !gostProcess.Running() || gost.CounterEpoch() != gostEpoch {
					t.Fatal("Xray update interrupted unchanged GOST process or reset counters")
				}
			}
			if err := adapter.Commit(ctx, second); !errors.Is(err, ErrLiveUpdateRequiresMaintenance) {
				t.Fatalf("unsupported update accepted: %v", err)
			}
			if err := adapter.Verify(ctx, first); err != nil {
				t.Fatal(err)
			}
			assertUnchanged()
			if !xrayProcess.Running() || !gostProcess.Running() || xray.CounterEpoch() != xrayEpoch || gost.CounterEpoch() != gostEpoch {
				t.Fatal("unsupported update interrupted a live engine")
			}
			if err := adapter.Rollback(ctx, &first); err != nil {
				t.Fatal(err)
			}
			if err := adapter.Verify(ctx, first); err != nil {
				t.Fatal(err)
			}
			assertUnchanged()
		})
	}
}

func TestMixedAdapterMetadataOnlyUpdatePreservesProcessesAndRecoversExitedEngine(t *testing.T) {
	ctx := context.Background()
	xray := newTestXrayAdapter(t, &recordingXrayRunner{}, true)
	gost := newTestGOSTAdapter(t, &recordingGOSTRunner{}, true)
	adapter, err := NewMixedProcessAdapter(xray, gost)
	if err != nil {
		t.Fatal(err)
	}
	fragments := mixedFragments("xray-one", "gost-one")
	first, err := adapter.Prepare(ctx, testDesired(t, 1, fragments))
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Commit(ctx, first); err != nil {
		t.Fatal(err)
	}
	xrayProcess, gostProcess := xray.process, gost.process
	xrayEpoch, gostEpoch := xray.CounterEpoch(), gost.CounterEpoch()
	for i := range fragments {
		fragments[i].GroupRevision++
	}
	second, err := adapter.Prepare(ctx, testDesired(t, 2, fragments))
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Commit(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Verify(ctx, second); err != nil {
		t.Fatal(err)
	}
	if xray.process != xrayProcess || gost.process != gostProcess || xray.CounterEpoch() != xrayEpoch || gost.CounterEpoch() != gostEpoch {
		t.Fatal("metadata-only update restarted an engine or reset counters")
	}
	for _, process := range []XrayProcess{xrayProcess, gostProcess} {
		if err := process.Stop(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := adapter.Commit(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Verify(ctx, second); err != nil {
		t.Fatal(err)
	}
	if xray.CounterEpoch() != xrayEpoch+1 || gost.CounterEpoch() != gostEpoch+1 {
		t.Fatal("identical native config prevented recovery of exited processes")
	}
}

func TestUnchangedEngineChecksStagedAndRunningConfiguration(t *testing.T) {
	for _, kind := range []agentv1.Engine{agentv1.EngineXray, agentv1.EngineGOST} {
		t.Run(string(kind), func(t *testing.T) {
			ctx := context.Background()
			var adapter EngineAdapter
			var activePath string
			var epoch func() uint64
			var compile func(agentv1.ConfigurationBundle) ([]byte, error)
			fragments := mixedFragments("xray-one", "gost-one")
			changed := mixedFragments("xray-two", "gost-two")
			if kind == agentv1.EngineXray {
				xray := newTestXrayAdapter(t, &recordingXrayRunner{}, true)
				adapter, activePath, epoch, compile = xray, xray.activePath, xray.CounterEpoch, CompileXrayBundle
				fragments, changed = fragments[:1], changed[:1]
			} else {
				gost := newTestGOSTAdapter(t, &recordingGOSTRunner{}, true)
				adapter, activePath, epoch, compile = gost, gost.activePath, gost.CounterEpoch, CompileGOSTBundle
				fragments, changed = fragments[1:], changed[1:]
			}
			first, err := adapter.Prepare(ctx, testDesired(t, 1, fragments))
			if err != nil {
				t.Fatal(err)
			}
			if err := adapter.Commit(ctx, first); err != nil {
				t.Fatal(err)
			}
			initialEpoch := epoch()
			second, err := adapter.Prepare(ctx, testDesired(t, 2, changed))
			if err != nil {
				t.Fatal(err)
			}
			data, err := compile(second.Bundle)
			if err != nil {
				t.Fatal(err)
			}
			// A matching file is insufficient: the running process still has
			// the first configuration, despite an external active-file change.
			if err := os.WriteFile(activePath, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := adapter.Commit(ctx, second); !errors.Is(err, ErrLiveUpdateRequiresMaintenance) {
				t.Fatalf("file drift accepted: %v", err)
			}
			if epoch() != initialEpoch {
				t.Fatal("file drift restarted the process")
			}
			if err := adapter.Verify(ctx, second); err == nil {
				t.Fatal("file drift falsely attested a running configuration")
			}
			original, err := os.ReadFile(first.adapterPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(activePath, original, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(second.adapterPath, []byte(`{}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := adapter.Commit(ctx, second); err == nil {
				t.Fatal("unchanged-process optimization accepted tampered staged config")
			}
			if err := adapter.Rollback(ctx, &second); err == nil {
				t.Fatal("rollback accepted tampered staged config")
			}
			if err := adapter.Verify(ctx, first); err != nil {
				t.Fatalf("invalid rollback interrupted the verified running process: %v", err)
			}
		})
	}
}
