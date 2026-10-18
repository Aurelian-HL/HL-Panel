package runtime

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hongle/hl-panel/internal/agent/config"
	"github.com/hongle/hl-panel/internal/agent/engine"
)

func TestEngineVersionsDoNotGuessConfiguredBinaries(t *testing.T) {
	cases := []struct {
		mode string
		want map[string]string
	}{
		{config.EngineModeMixed, map[string]string{"node-bundle": "mixed-process-adapter", "xray": "unknown", "gost": "unknown"}},
		{config.EngineModeXray, map[string]string{"node-bundle": "xray-process-adapter", "xray": "unknown"}},
		{config.EngineModeGOST, map[string]string{"node-bundle": "gost-process-adapter", "gost": "unknown"}},
		{config.EngineModeDryRun, map[string]string{"node-bundle": "dry-run-file-adapter"}},
	}
	for _, tc := range cases {
		a := &Agent{engineMode: tc.mode}
		if got := a.engineVersions(); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("mode=%s: versions=%v, want %v", tc.mode, got, tc.want)
		}
	}
}

type versionXrayRunner struct{}

func (versionXrayRunner) Test(context.Context, string) error                        { return nil }
func (versionXrayRunner) Start(context.Context, string) (engine.XrayProcess, error) { return nil, nil }
func (versionXrayRunner) EngineVersion() string                                     { return "26.9.25" }

type capabilityXrayRunner struct{ version string }

func (capabilityXrayRunner) Test(context.Context, string) error { return nil }
func (capabilityXrayRunner) Start(context.Context, string) (engine.XrayProcess, error) {
	return nil, nil
}
func (r capabilityXrayRunner) EngineVersion() string { return r.version }

type versionGOSTRunner struct{}

func (versionGOSTRunner) Test(context.Context, string) error                        { return nil }
func (versionGOSTRunner) Start(context.Context, string) (engine.GOSTProcess, error) { return nil, nil }
func (versionGOSTRunner) EngineVersion() string                                     { return "3.3.1" }

func TestEngineVersionsReadAdapterAttestation(t *testing.T) {
	root := t.TempDir()
	xray, err := engine.NewXrayProcessAdapter(engine.XrayAdapterOptions{
		StagingDir: filepath.Join(root, "xray-staging"), ActivePath: filepath.Join(root, "xray.json"), Runner: versionXrayRunner{},
	})
	if err != nil {
		t.Fatal(err)
	}
	gost, err := engine.NewGOSTProcessAdapter(engine.GOSTAdapterOptions{
		StagingDir: filepath.Join(root, "gost-staging"), ActivePath: filepath.Join(root, "gost.json"), Runner: versionGOSTRunner{},
	})
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{engineMode: config.EngineModeMixed, xrayAdapter: xray, gostAdapter: gost}
	for i := 0; i < 10; i++ {
		versions := a.engineVersions()
		if versions["xray"] != "26.9.25" || versions["gost"] != "3.3.1" {
			t.Fatalf("versions=%v", versions)
		}
		// The caller must never be able to alter future heartbeat telemetry.
		versions["xray"] = "changed"
	}
}

func TestVLESSUDPDirectCapabilityRequiresManagedVerifiedXrayCore(t *testing.T) {
	for _, test := range []struct {
		name, mode, version string
		autoStart           bool
		want                bool
	}{
		{"verified xray", config.EngineModeXray, "26.7.28", true, true},
		{"verified mixed", config.EngineModeMixed, "26.7.28", true, true},
		{"unknown version", config.EngineModeXray, "unknown", true, false},
		{"different version", config.EngineModeXray, "26.9.25", true, false},
		{"unmanaged xray", config.EngineModeXray, "26.7.28", false, false},
		{"gost mode", config.EngineModeGOST, "26.7.28", true, false},
		{"dry run", config.EngineModeDryRun, "26.7.28", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			xray, err := engine.NewXrayProcessAdapter(engine.XrayAdapterOptions{
				StagingDir: filepath.Join(root, "staging"), ActivePath: filepath.Join(root, "xray.json"),
				Runner: capabilityXrayRunner{version: test.version}, AutoStart: test.autoStart,
			})
			if err != nil {
				t.Fatal(err)
			}
			a := &Agent{
				cfg:        config.Config{EngineMode: test.mode, Capabilities: []string{config.CapabilityXrayVLESSUDPDirect}},
				engineMode: test.mode, xrayAdapter: xray,
			}
			got := false
			for _, capability := range a.capabilities() {
				got = got || capability == config.CapabilityXrayVLESSUDPDirect
			}
			if got != test.want {
				t.Fatalf("capability = %v, want %v", got, test.want)
			}
		})
	}
}
