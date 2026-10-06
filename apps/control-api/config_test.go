package main

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hongle/hl-panel/internal/control/auth"
)

func TestHashPasswordCommandReadsStdinAndWritesOnlyHash(t *testing.T) {
	var output bytes.Buffer
	if err := hashPasswordFromReader(strings.NewReader("not-logged-secret\n"), &output); err != nil {
		t.Fatalf("hash password: %v", err)
	}
	encoded := strings.TrimSpace(output.String())
	if strings.Contains(encoded, "not-logged-secret") {
		t.Fatal("hash command returned the plaintext password")
	}
	if err := auth.ValidatePasswordHash([]byte(encoded)); err != nil {
		t.Fatalf("hash command returned invalid hash: %v", err)
	}
}

func TestRuntimeConfigRequiresExplicitAdministratorCredential(t *testing.T) {
	clearRuntimeEnvironment(t)
	t.Setenv("CONTROL_BOOTSTRAP_ADMIN_USERNAME", "admin")
	if _, err := loadRuntimeConfig(); err == nil {
		t.Fatal("loadRuntimeConfig succeeded without a password source")
	}
}

func TestRuntimeConfigRefusesPlaintextNonLoopbackByDefault(t *testing.T) {
	clearRuntimeEnvironment(t)
	t.Setenv("CONTROL_BOOTSTRAP_ADMIN_USERNAME", "admin")
	t.Setenv("CONTROL_BOOTSTRAP_ADMIN_PASSWORD", "explicit-local-secret")
	t.Setenv("CONTROL_LISTEN_ADDRESS", "0.0.0.0:8080")
	t.Setenv("CONTROL_ALLOW_VOLATILE_STORE", "true")
	if _, err := loadRuntimeConfig(); err == nil {
		t.Fatal("loadRuntimeConfig allowed a plaintext non-loopback listener")
	}
}

func clearRuntimeEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"CONTROL_LISTEN_ADDRESS",
		"CONTROL_TLS_CERT_FILE",
		"CONTROL_TLS_KEY_FILE",
		"CONTROL_ALLOW_INSECURE_HTTP",
		"CONTROL_ALLOW_VOLATILE_STORE",
		"CONTROL_DATABASE_URL",
		"CONTROL_DATABASE_URL_FILE",
		"CONTROL_BOOTSTRAP_ADMIN_USERNAME",
		"CONTROL_BOOTSTRAP_ADMIN_PASSWORD_HASH",
		"CONTROL_BOOTSTRAP_ADMIN_PASSWORD_FILE",
		"CONTROL_BOOTSTRAP_ADMIN_PASSWORD",
		"CONTROL_CUSTOMER_PASSWORD_FINGERPRINT_KEY_B64",
		"CONTROL_CUSTOMER_PASSWORD_FINGERPRINT_KEY_FILE",
		"CONTROL_REALITY_SERVER_NAME",
		"CONTROL_REALITY_DESTINATION",
		"CONTROL_NEZHA_DASHBOARD_URL",
		"CONTROL_NEZHA_PAT_FILE",
		"CONTROL_NEZHA_NODE_MAP_FILE",
		"CONTROL_NEZHA_TIMEOUT",
	} {
		t.Setenv(name, "")
	}
}

func TestNezhaMonitorConfigUsesProtectedFilesAndLoopback(t *testing.T) {
	clearRuntimeEnvironment(t)
	pat := "private-monitor-pat-123456789"
	patFile := filepath.Join(t.TempDir(), "nezha-pat")
	if err := os.WriteFile(patFile, []byte(pat+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mapFile := filepath.Join(t.TempDir(), "nezha-map.json")
	if err := os.WriteFile(mapFile, []byte(`{"node-1":12}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONTROL_NEZHA_PAT_FILE", patFile)
	t.Setenv("CONTROL_NEZHA_NODE_MAP_FILE", mapFile)
	t.Setenv("CONTROL_NEZHA_DASHBOARD_URL", "http://example.com:8008")
	if _, err := loadNezhaMonitor(); err == nil || strings.Contains(err.Error(), pat) {
		t.Fatalf("unsafe Nezha URL accepted or PAT leaked: %v", err)
	}
	t.Setenv("CONTROL_NEZHA_DASHBOARD_URL", "http://127.0.0.1:8008")
	if service, err := loadNezhaMonitor(); err != nil || service == nil {
		t.Fatalf("valid Nezha monitoring config rejected: %v", err)
	}
	if err := os.WriteFile(mapFile, []byte(`{"node-1":12,"node-2":12}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadNezhaMonitor(); err == nil {
		t.Fatal("duplicate server mapping accepted")
	}
}

func TestNezhaPATRequiresPrivateModeOnUnix(t *testing.T) {
	for _, tc := range []struct {
		mode os.FileMode
		want bool
	}{
		{0o600, true},
		{0o640, true},
		{0o400, true},
		{0o644, false},
		{0o660, false},
		{0o700, false},
	} {
		if got := nezhaPOSIXPATPermissionsSafe(tc.mode); got != tc.want {
			t.Errorf("mode %o: got %t, want %t", tc.mode, got, tc.want)
		}
	}
	if runtime.GOOS == "windows" {
		return // os.FileMode does not describe Windows ACLs.
	}
	path := filepath.Join(t.TempDir(), "nezha-pat")
	if err := os.WriteFile(path, []byte("private-monitor-pat-123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readNezhaPAT(path); err == nil {
		t.Fatal("world-readable Nezha PAT was accepted")
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := readNezhaPAT(path); err != nil {
		t.Fatalf("protected Nezha PAT was rejected: %v", err)
	}
}

func TestRuntimeConfigValidatesAutomaticRealityTarget(t *testing.T) {
	clearRuntimeEnvironment(t)
	t.Setenv("CONTROL_BOOTSTRAP_ADMIN_USERNAME", "admin")
	t.Setenv("CONTROL_BOOTSTRAP_ADMIN_PASSWORD", "explicit-local-secret")
	t.Setenv("CONTROL_ALLOW_VOLATILE_STORE", "true")
	setCustomerFingerprintKey(t)
	t.Setenv("CONTROL_REALITY_SERVER_NAME", "example.com")
	if _, err := loadRuntimeConfig(); err == nil || !strings.Contains(err.Error(), "automatic Reality") {
		t.Fatalf("partial Reality defaults accepted: %v", err)
	}
	t.Setenv("CONTROL_REALITY_DESTINATION", "example.com:0")
	if _, err := loadRuntimeConfig(); err == nil || !strings.Contains(err.Error(), "automatic Reality") {
		t.Fatalf("invalid Reality destination accepted: %v", err)
	}
	t.Setenv("CONTROL_REALITY_DESTINATION", "example.com:443")
	config, err := loadRuntimeConfig()
	if err != nil || config.RealityDefaults.ServerName != "example.com" || config.RealityDefaults.Destination != "example.com:443" {
		t.Fatalf("valid Reality defaults were not loaded: %+v, %v", config.RealityDefaults, err)
	}
}

func TestRuntimeConfigDurableStoreDoesNotRequireVolatileOptIn(t *testing.T) {
	clearRuntimeEnvironment(t)
	t.Setenv("CONTROL_BOOTSTRAP_ADMIN_USERNAME", "admin")
	t.Setenv("CONTROL_BOOTSTRAP_ADMIN_PASSWORD", "explicit-local-secret")
	t.Setenv("CONTROL_DATABASE_URL", "postgres://localhost/nyvp_trial?sslmode=disable")
	setCustomerFingerprintKey(t)
	config, err := loadRuntimeConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.AllowVolatileStore || config.DatabaseURL == "" {
		t.Fatal("durable store configuration was not preserved")
	}
}

func TestRuntimeConfigRequiresIndependentCustomerPasswordFingerprintKey(t *testing.T) {
	clearRuntimeEnvironment(t)
	t.Setenv("CONTROL_BOOTSTRAP_ADMIN_USERNAME", "admin")
	t.Setenv("CONTROL_BOOTSTRAP_ADMIN_PASSWORD", "explicit-local-secret")
	t.Setenv("CONTROL_ALLOW_VOLATILE_STORE", "true")
	if _, err := loadRuntimeConfig(); err == nil || !strings.Contains(err.Error(), "customer password fingerprint key") {
		t.Fatalf("missing customer fingerprint key was accepted: %v", err)
	}
	t.Setenv("CONTROL_CUSTOMER_PASSWORD_FINGERPRINT_KEY_B64", base64.StdEncoding.EncodeToString([]byte("too-short")))
	if _, err := loadRuntimeConfig(); err == nil || !strings.Contains(err.Error(), "at least 32") {
		t.Fatalf("short customer fingerprint key was accepted: %v", err)
	}
}

func TestRuntimeConfigLoadsCustomerPasswordFingerprintKeyFromFile(t *testing.T) {
	clearRuntimeEnvironment(t)
	t.Setenv("CONTROL_BOOTSTRAP_ADMIN_USERNAME", "admin")
	t.Setenv("CONTROL_BOOTSTRAP_ADMIN_PASSWORD", "explicit-local-secret")
	t.Setenv("CONTROL_ALLOW_VOLATILE_STORE", "true")
	want := []byte("0123456789abcdef0123456789abcdef")
	path := filepath.Join(t.TempDir(), "customer-fingerprint-key")
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(want)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONTROL_CUSTOMER_PASSWORD_FINGERPRINT_KEY_FILE", path)
	config, err := loadRuntimeConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(config.CustomerPasswordFingerprintKey, want) {
		t.Fatal("customer fingerprint key file was not loaded exactly")
	}
}

func setCustomerFingerprintKey(t *testing.T) {
	t.Helper()
	t.Setenv("CONTROL_CUSTOMER_PASSWORD_FINGERPRINT_KEY_B64", base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
}

func TestRuntimeConfigRefusesUnconfiguredStore(t *testing.T) {
	clearRuntimeEnvironment(t)
	t.Setenv("CONTROL_BOOTSTRAP_ADMIN_USERNAME", "admin")
	t.Setenv("CONTROL_BOOTSTRAP_ADMIN_PASSWORD", "explicit-local-secret")
	if _, err := loadRuntimeConfig(); err == nil {
		t.Fatal("unconfigured persistence accepted")
	}
}
