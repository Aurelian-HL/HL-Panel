package xray_test

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/hongle/hl-panel/internal/agent/engine"
)

func TestXrayInstalledVersionIsCachedWithoutHeartbeatProcesses(t *testing.T) {
	binary := xrayBinary(t)
	input, err := os.Open(binary)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	copyPath := filepath.Join(t.TempDir(), filepath.Base(binary))
	output, err := os.OpenFile(copyPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatal("copy isolated binary failed")
	}
	runner, err := engine.NewExecXrayRunner(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	version := runner.EngineVersion()
	if version == "unknown" || version == "configured" || version == "" {
		t.Fatal("real binary version not detected")
	}
	// Removing only the test copy proves later reads do not execute the binary.
	if err := os.Remove(copyPath); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	adapter, err := engine.NewXrayProcessAdapter(engine.XrayAdapterOptions{
		StagingDir: filepath.Join(root, "staging"), ActivePath: filepath.Join(root, "active.json"), Runner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if runner.EngineVersion() != version || adapter.EngineVersion() != version {
			t.Fatal("cached version changed")
		}
	}
	t.Logf("installed Xray version %s cached", version)
}
