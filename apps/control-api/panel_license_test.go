package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func pinTestLicense(t *testing.T) {
	t.Helper()
	oldURL, oldID, oldKeys, oldB64 := panelLicenseIssuerURL, panelLicenseIssuerID, panelLicensePublicKeys, panelLicensePublicKeysB64
	t.Cleanup(func() {
		panelLicenseIssuerURL, panelLicenseIssuerID, panelLicensePublicKeys, panelLicensePublicKeysB64 = oldURL, oldID, oldKeys, oldB64
	})
	panelLicenseIssuerURL, panelLicenseIssuerID = "https://issuer.example.test", "https://issuer.example.test"
	panelLicensePublicKeys = `{"test":"` + base64.RawURLEncoding.EncodeToString(ed25519.NewKeyFromSeed(make([]byte, 32)).Public().(ed25519.PublicKey)) + `"}`
	panelLicensePublicKeysB64 = ""
}

func TestLicenseTrustFailsClosedWithoutCreatingIdentity(t *testing.T) {
	pinTestLicense(t)
	directory := t.TempDir()
	t.Setenv("CONTROL_LICENSE_STATE_DIR", directory)
	valid := panelLicensePublicKeys
	for _, tc := range []struct{ name, url, id, keys string }{
		{"empty map", "https://issuer.example.test", "https://issuer.example.test", "{}"},
		{"null", "https://issuer.example.test", "https://issuer.example.test", "null"},
		{"empty key", "https://issuer.example.test", "https://issuer.example.test", `{"":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`},
		{"plaintext", "http://issuer.example.test", "https://issuer.example.test", valid},
		{"missing id", "https://issuer.example.test", "", valid},
		{"query", "https://issuer.example.test/?token=secret", "https://issuer.example.test", valid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			panelLicenseIssuerURL, panelLicenseIssuerID, panelLicensePublicKeys = tc.url, tc.id, tc.keys
			if err := panelLicenseCommand([]string{"trust"}, bytes.NewReader(nil), &bytes.Buffer{}); err == nil {
				t.Fatal("invalid trust accepted")
			}
		})
	}
	panelLicenseIssuerURL, panelLicenseIssuerID, panelLicensePublicKeys = "https://issuer.example.test", "https://issuer.example.test", valid
	var output bytes.Buffer
	if err := panelLicenseCommand([]string{"trust"}, bytes.NewReader(nil), &output); err != nil {
		t.Fatal(err)
	}
	files, _ := os.ReadDir(directory)
	if len(files) != 0 {
		t.Fatal("trust command created instance state")
	}
}

func TestPinnedLicenseCannotBeDisabledOrReplacedByEnvironment(t *testing.T) {
	pinTestLicense(t)
	t.Setenv("CONTROL_LICENSE_V2", "false")
	t.Setenv("CONTROL_LICENSE_V2_ISSUER_URL", "https://attacker.example.test")
	t.Setenv("CONTROL_LICENSE_V2_ISSUER_ID", "https://attacker.example.test")
	t.Setenv("CONTROL_LICENSE_V2_PUBLIC_KEYS", "null")
	if !signedLicenseEnabled() {
		t.Fatal("environment disabled pinned licensing")
	}
	config, err := signedLicenseConfig("panel.example.test", "instance-test", t.TempDir())
	if err != nil || config.IssuerURL != "https://issuer.example.test" {
		t.Fatal("environment replaced pinned trust", err)
	}
	panelLicenseIssuerURL = ""
	if !signedLicenseEnabled() {
		t.Fatal("partial pin disabled enforcement")
	}
	if _, err := signedLicenseConfig("panel.example.test", "instance-test", t.TempDir()); err == nil {
		t.Fatal("partial pin fell back to runtime trust")
	}
}

func TestSidecarIdentityAndMissingConfigurationPreserveRestrictedStartup(t *testing.T) {
	pinTestLicense(t)
	directory := t.TempDir()
	t.Setenv("CONTROL_LICENSE_DOMAIN", "")
	t.Setenv("CONTROL_LICENSE_INSTANCE_ID", "")
	t.Setenv("CONTROL_LICENSE_STATE_DIR", directory)
	var configuration runtimeConfig
	if err := loadLicenseConfig(&configuration); err != nil {
		t.Fatal(err)
	}
	if status := configuration.PanelLicense.Status(""); status.Status != "unconfigured" || status.ManagementAllowed || !configuration.LicenseEnforce {
		t.Fatalf("unsafe fallback: %+v", status)
	}
	raw, _ := json.Marshal(map[string]string{"domain": "panel.example.test", "instance_id": "hlp-isolated-test"})
	if err := os.WriteFile(filepath.Join(directory, "identity.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	domain, instance, found := licenseRuntimeIdentity()
	if domain != "panel.example.test" || instance != "hlp-isolated-test" || found != directory {
		t.Fatal("sidecar identity not loaded")
	}
	t.Setenv("CONTROL_LICENSE_DOMAIN", "explicit.example.test")
	domain, _, _ = licenseRuntimeIdentity()
	if domain != "explicit.example.test" {
		t.Fatal("sidecar overwrote explicit identity")
	}
}

func TestLicenseCLIReadsIdentityFromExplicitStateDirectory(t *testing.T) {
	pinTestLicense(t)
	defaultDirectory := t.TempDir()
	t.Setenv("CONTROL_LICENSE_STATE_DIR", defaultDirectory)
	t.Setenv("CONTROL_LICENSE_DOMAIN", "")
	t.Setenv("CONTROL_LICENSE_INSTANCE_ID", "")
	if err := os.WriteFile(filepath.Join(defaultDirectory, "identity.json"), []byte(`{"domain":"other.example.test","instance_id":"hlp-other-directory"}`), 0600); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	raw := []byte(`{"domain":"panel.example.test","instance_id":"hlp-explicit-directory"}`)
	if err := os.WriteFile(filepath.Join(directory, "identity.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := panelLicenseCommand([]string{"status", "--state-dir", directory}, bytes.NewReader(nil), &output); err != nil {
		t.Fatal(err)
	}
	var status struct {
		Domain            string `json:"domain"`
		ManagementAllowed bool   `json:"management_allowed"`
	}
	if err := json.Unmarshal(output.Bytes(), &status); err != nil || status.Domain != "panel.example.test" || status.ManagementAllowed {
		t.Fatal("CLI failed to use explicit identity or granted unactivated access", err)
	}
}

func TestAutomaticFreeRequiresMatchingInstallerIdentity(t *testing.T) {
	pinTestLicense(t)
	for _, tc := range []struct {
		name, raw string
		want      bool
	}{
		{"new automatic install", `{"domain":"panel.example.test","instance_id":"hlp-auto-test","auto_free":true}`, true},
		{"older install", `{"domain":"panel.example.test","instance_id":"hlp-auto-test"}`, false},
		{"manual install", `{"domain":"panel.example.test","instance_id":"hlp-auto-test","auto_free":false}`, false},
		{"other domain", `{"domain":"other.example.test","instance_id":"hlp-auto-test","auto_free":true}`, false},
		{"other instance", `{"domain":"panel.example.test","instance_id":"hlp-other-test","auto_free":true}`, false},
		{"corrupt identity", `{"auto_free":true`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.WriteFile(filepath.Join(directory, "identity.json"), []byte(tc.raw), 0600); err != nil {
				t.Fatal(err)
			}
			configuration, err := signedLicenseConfig("panel.example.test", "hlp-auto-test", directory)
			if err != nil || configuration.AutoRegisterFree != tc.want {
				t.Fatalf("automatic install intent: got %v, want %v: %v", configuration.AutoRegisterFree, tc.want, err)
			}
		})
	}
}
