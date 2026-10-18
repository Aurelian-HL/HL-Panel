package config

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultHeartbeatSamplesEverySecond(t *testing.T) {
	cfg := Config{}
	cfg.setDefaults()
	if cfg.HeartbeatInterval.Duration() != time.Second {
		t.Fatal("default probe cadence must be 1s")
	}
}

func TestNodeCapabilitiesOnlyAdvertiseActiveBBR2(t *testing.T) {
	original := tcpBBR2Enabled
	t.Cleanup(func() { tcpBBR2Enabled = original })

	tcpBBR2Enabled = func() bool { return true }
	cfg := Config{Capabilities: []string{"custom"}}
	cfg.setDefaults()
	if !hasCapability(cfg.NodeCapabilities(), CapabilityTCPBBR2) {
		t.Fatalf("active BBR2 capability missing: %v", cfg.NodeCapabilities())
	}

	tcpBBR2Enabled = func() bool { return false }
	cfg.Capabilities = append(cfg.Capabilities, CapabilityTCPBBR2)
	if hasCapability(cfg.NodeCapabilities(), CapabilityTCPBBR2) {
		t.Fatalf("disabled BBR2 capability was advertised: %v", cfg.NodeCapabilities())
	}
}

func TestHTTPClientUsesBoundedLongLivedTransport(t *testing.T) {
	cfg := Config{RequestTimeout: Duration(17 * time.Second)}
	client, err := cfg.HTTPClient()
	if err != nil {
		t.Fatalf("HTTPClient() error = %v", err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("HTTPClient() transport = %T, want *http.Transport", client.Transport)
	}
	if transport.MaxIdleConns != 100 || transport.MaxIdleConnsPerHost != 16 || transport.MaxConnsPerHost != 32 {
		t.Fatalf("transport connection limits = (%d, %d, %d), want (100, 16, 32)", transport.MaxIdleConns, transport.MaxIdleConnsPerHost, transport.MaxConnsPerHost)
	}
	if transport.IdleConnTimeout != 25*time.Second || transport.TLSHandshakeTimeout != 10*time.Second || transport.ResponseHeaderTimeout != 17*time.Second {
		t.Fatalf("transport timeouts = idle %s, tls %s, header %s", transport.IdleConnTimeout, transport.TLSHandshakeTimeout, transport.ResponseHeaderTimeout)
	}
	if client.Timeout != 17*time.Second {
		t.Fatalf("client timeout = %s, want 17s", client.Timeout)
	}
}

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
