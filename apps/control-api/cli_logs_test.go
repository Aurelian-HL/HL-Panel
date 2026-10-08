package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hongle/hl-panel/internal/control/auth"
)

func TestHashPasswordCLIDoesNotRewriteServiceLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.log")
	original := []byte("existing service log\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestPanelCLIHelper$")
	command.Env = append(os.Environ(), "HL_PANEL_CLI_TEST=1", "CONTROL_PANEL_LOG_FILE="+path)
	command.Stdin = strings.NewReader("isolated-cli-test\n")
	output, err := command.CombinedOutput()
	if err != nil || auth.VerifyPassword(bytes.TrimSpace(output), "isolated-cli-test") != nil {
		t.Fatal("hash-password CLI did not produce a password hash", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(original, got) {
		t.Fatal("CLI rewrote the service log", err)
	}
}

func TestPanelCLIHelper(t *testing.T) {
	if os.Getenv("HL_PANEL_CLI_TEST") != "1" {
		return
	}
	os.Args = []string{"control-api", "hash-password"}
	main()
	os.Exit(0)
}
