package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateRequiresTLSExceptExplicitLoopback(t *testing.T) {
	t.Parallel()
	base := Config{
		ControlPlaneURL:     "https://control.example",
		DataDir:             filepath.Join(t.TempDir(), "agent"),
		Hostname:            "edge-1",
		HeartbeatInterval:   Duration(time.Second),
		DesiredPollInterval: Duration(time.Second),
		RequestTimeout:      Duration(time.Second),
		Backoff:             BackoffConfig{Initial: Duration(time.Second), Maximum: Duration(time.Second), Multiplier: 1},
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("HTTPS config rejected: %v", err)
	}
	base.ControlPlaneURL = "http://127.0.0.1:8080"
	if err := base.Validate(); err == nil {
		t.Fatalf("plain HTTP config accepted without explicit loopback flag")
	}
	base.AllowInsecureLoopback = true
	if err := base.Validate(); err != nil {
		t.Fatalf("explicit loopback HTTP rejected: %v", err)
	}
	base.ControlPlaneURL = "http://control.example"
	if err := base.Validate(); err == nil {
		t.Fatalf("plain HTTP non-loopback config accepted")
	}
}

func TestValidateRejectsControlPlanePath(t *testing.T) {
	t.Parallel()
	config := Config{ControlPlaneURL: "https://control.example/api", DataDir: filepath.Join(t.TempDir(), "agent"), Hostname: "edge-1", HeartbeatInterval: Duration(time.Second), DesiredPollInterval: Duration(time.Second), RequestTimeout: Duration(time.Second), Backoff: BackoffConfig{Initial: Duration(time.Second), Maximum: Duration(time.Second), Multiplier: 1}}
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "must not contain a path") {
		t.Fatalf("Validate() error = %v, want path validation error", err)
	}
}

func TestValidateEngineModeRequiresExplicitXrayBinary(t *testing.T) {
	t.Parallel()
	base := Config{ControlPlaneURL: "https://control.example", DataDir: filepath.Join(t.TempDir(), "agent"), Hostname: "edge-1", HeartbeatInterval: Duration(time.Second), DesiredPollInterval: Duration(time.Second), RequestTimeout: Duration(time.Second), Backoff: BackoffConfig{Initial: Duration(time.Second), Maximum: Duration(time.Second), Multiplier: 1}}
	base.EngineMode = EngineModeXray
	if err := base.Validate(); err == nil || !strings.Contains(err.Error(), "xray_binary_path") {
		t.Fatalf("xray mode without binary path error = %v", err)
	}
	base.EngineMode = EngineModeDryRun
	base.XrayAutoStart = true
	if err := base.Validate(); err == nil || !strings.Contains(err.Error(), "xray_auto_start") {
		t.Fatalf("dry-run auto-start error = %v", err)
	}
	base.XrayAutoStart = false
	base.EngineMode = "unknown"
	if err := base.Validate(); err == nil || !strings.Contains(err.Error(), "engine_mode") {
		t.Fatalf("unknown engine mode error = %v", err)
	}
}

func TestValidateEngineModeRequiresExplicitGOSTBinary(t *testing.T) {
	t.Parallel()
	base := Config{ControlPlaneURL: "https://control.example", DataDir: filepath.Join(t.TempDir(), "agent"), Hostname: "edge-1", HeartbeatInterval: Duration(time.Second), DesiredPollInterval: Duration(time.Second), RequestTimeout: Duration(time.Second), Backoff: BackoffConfig{Initial: Duration(time.Second), Maximum: Duration(time.Second), Multiplier: 1}}
	base.EngineMode = EngineModeGOST
	if err := base.Validate(); err == nil || !strings.Contains(err.Error(), "gost_binary_path") {
		t.Fatalf("gost mode without binary path error = %v", err)
	}
	base.GOSTBinaryPath = "gost"
	if err := base.Validate(); err == nil || !strings.Contains(err.Error(), "gost_binary_path") {
		t.Fatalf("gost mode with relative binary path error = %v", err)
	}
	base.GOSTBinaryPath = filepath.Join(base.DataDir, "gost")
	if err := base.Validate(); err != nil {
		t.Fatalf("gost mode with absolute binary path rejected: %v", err)
	}
	base.EngineMode = EngineModeDryRun
	base.GOSTAutoStart = true
	if err := base.Validate(); err == nil || !strings.Contains(err.Error(), "gost_auto_start") {
		t.Fatalf("dry-run gost auto-start error = %v", err)
	}
}

func TestValidateMixedEngineRequiresBothOwnedProcesses(t *testing.T) {
	base := Config{ControlPlaneURL: "https://control.example", DataDir: filepath.Join(t.TempDir(), "agent"), Hostname: "edge-1", HeartbeatInterval: Duration(time.Second), DesiredPollInterval: Duration(time.Second), RequestTimeout: Duration(time.Second), Backoff: BackoffConfig{Initial: Duration(time.Second), Maximum: Duration(time.Second), Multiplier: 1}}
	base.EngineMode = EngineModeMixed
	if err := base.Validate(); err == nil || !strings.Contains(err.Error(), "xray_binary_path") {
		t.Fatalf("mixed mode without xray binary = %v", err)
	}
	base.XrayBinaryPath = filepath.Join(base.DataDir, "xray")
	if err := base.Validate(); err == nil || !strings.Contains(err.Error(), "gost_binary_path") {
		t.Fatalf("mixed mode without gost binary = %v", err)
	}
	base.GOSTBinaryPath = filepath.Join(base.DataDir, "gost")
	if err := base.Validate(); err == nil || !strings.Contains(err.Error(), "both xray_auto_start and gost_auto_start") {
		t.Fatalf("mixed mode without owned processes = %v", err)
	}
	base.XrayAutoStart = true
	if err := base.Validate(); err == nil || !strings.Contains(err.Error(), "both xray_auto_start and gost_auto_start") {
		t.Fatalf("mixed mode with only xray owned = %v", err)
	}
	base.GOSTAutoStart = true
	if err := base.Validate(); err != nil {
		t.Fatalf("mixed mode with both owned processes rejected: %v", err)
	}
	base.setDefaults()
	if !hasCapability(base.Capabilities, CapabilityXrayVLESSReality) || !hasCapability(base.Capabilities, CapabilityGOSTTCPDirect) {
		t.Fatalf("mixed mode capabilities = %v", base.Capabilities)
	}
}

func hasCapability(capabilities []string, candidate string) bool {
	for _, capability := range capabilities {
		if capability == candidate {
			return true
		}
	}
	return false
}
