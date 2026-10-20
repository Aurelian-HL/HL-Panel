package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/panellicense"
)

// Commercial artifacts pin all three at build time. Runtime environment cannot
// override their trust root or turn enforcement off.
var panelLicenseIssuerURL string
var panelLicenseIssuerID string
var panelLicensePublicKeys string    // JSON object: key ID -> raw base64url public key
var panelLicensePublicKeysB64 string // Raw base64url JSON avoids linker/shell quoting.

func signedLicenseEnabled() bool {
	return pinnedLicenseTrust() || os.Getenv("CONTROL_LICENSE_V2") == "true"
}
func pinnedLicenseTrust() bool {
	return panelLicenseIssuerURL != "" || panelLicenseIssuerID != "" || panelLicensePublicKeys != "" || panelLicensePublicKeysB64 != ""
}
func signedLicenseConfig(domain, instance, directory string) (panellicense.Config, error) {
	u, id, keysJSON := panelLicenseIssuerURL, panelLicenseIssuerID, panelLicensePublicKeys
	if panelLicensePublicKeysB64 != "" {
		raw, err := base64.RawURLEncoding.DecodeString(panelLicensePublicKeysB64)
		if err != nil {
			return panellicense.Config{}, errors.New("invalid pinned issuer key encoding")
		}
		keysJSON = string(raw)
	}
	if !pinnedLicenseTrust() {
		u = os.Getenv("CONTROL_LICENSE_V2_ISSUER_URL")
		id = os.Getenv("CONTROL_LICENSE_V2_ISSUER_ID")
		keysJSON = os.Getenv("CONTROL_LICENSE_V2_PUBLIC_KEYS")
	}
	var encoded map[string]string
	if err := json.Unmarshal([]byte(keysJSON), &encoded); err != nil {
		return panellicense.Config{}, errors.New("signed licensing requires pinned issuer keys")
	}
	keys := make(map[string]ed25519.PublicKey, len(encoded))
	for id, value := range encoded {
		raw, err := base64.RawURLEncoding.DecodeString(value)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return panellicense.Config{}, errors.New("invalid pinned issuer key")
		}
		keys[id] = ed25519.PublicKey(raw)
	}
	configuration := panellicense.Config{IssuerURL: u, IssuerID: id, Keys: keys, Domain: domain, InstanceID: instance, Directory: directory, Timeout: 10 * time.Second}
	if err := panellicense.ValidateConfig(configuration); err != nil {
		return panellicense.Config{}, err
	}
	return configuration, nil
}
func loadSignedLicense() (*panellicense.Client, error) {
	domain, instance, directory := licenseRuntimeIdentity()
	configuration, err := signedLicenseConfig(domain, instance, directory)
	if err != nil {
		return nil, err
	}
	return panellicense.NewClient(configuration)
}

// A sidecar identity lets the first licensed release be prepared without
// changing legacy environment fields or restarting the running old panel.
func licenseRuntimeIdentity() (domain, instance, directory string) {
	domain, instance, directory = os.Getenv("CONTROL_LICENSE_DOMAIN"), os.Getenv("CONTROL_LICENSE_INSTANCE_ID"), os.Getenv("CONTROL_LICENSE_STATE_DIR")
	if directory == "" {
		directory = "/var/lib/hl-panel-license"
	}
	domain, instance = licenseSidecarIdentity(domain, instance, directory)
	return
}

func licenseSidecarIdentity(domain, instance, directory string) (string, string) {
	path := filepath.Join(directory, "identity.json")
	info, err := os.Lstat(path)
	if err == nil && info.Mode().IsRegular() && info.Size() <= 2048 {
		var identity struct {
			Domain     string `json:"domain"`
			InstanceID string `json:"instance_id"`
		}
		if raw, err := os.ReadFile(path); err == nil && json.Unmarshal(raw, &identity) == nil {
			if domain == "" {
				domain = identity.Domain
			}
			if instance == "" {
				instance = identity.InstanceID
			}
		}
	}
	return domain, instance
}

func panelLicenseCommand(arguments []string, input io.Reader, output io.Writer) error {
	if len(arguments) == 0 {
		return errors.New("usage: control-api license {trust|prepare|commit|status|sync} --domain DOMAIN --instance ID --state-dir DIRECTORY")
	}
	if arguments[0] == "trust" {
		if panelLicenseIssuerURL == "" || panelLicenseIssuerID == "" || (panelLicensePublicKeys == "" && panelLicensePublicKeysB64 == "") {
			return errors.New("发行包未配置可信授权服务，请先完成 HT 授权服务配置并构建授权版")
		}
		if _, err := signedLicenseConfig("panel.example.com", "trust-validation", os.TempDir()); err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(map[string]any{"version": 2, "issuer_url": panelLicenseIssuerURL, "issuer_id": panelLicenseIssuerID})
	}
	flags := flag.NewFlagSet("license", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	// Read a sidecar only after parsing --state-dir, so the default directory's
	// identity cannot leak into a command targeting another installation.
	defaultDirectory := os.Getenv("CONTROL_LICENSE_STATE_DIR")
	if defaultDirectory == "" {
		defaultDirectory = "/var/lib/hl-panel-license"
	}
	domain := flags.String("domain", os.Getenv("CONTROL_LICENSE_DOMAIN"), "")
	instance := flags.String("instance", os.Getenv("CONTROL_LICENSE_INSTANCE_ID"), "")
	directory := flags.String("state-dir", defaultDirectory, "")
	if err := flags.Parse(arguments[1:]); err != nil || flags.NArg() != 0 {
		return errors.New("invalid license command arguments")
	}
	*domain, *instance = licenseSidecarIdentity(*domain, *instance, *directory)
	configuration, err := signedLicenseConfig(*domain, *instance, *directory)
	if err != nil {
		return err
	}
	client, err := panellicense.NewClient(configuration)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	switch arguments[0] {
	case "prepare":
		raw, err := io.ReadAll(io.LimitReader(input, 514))
		if err != nil || len(raw) > 513 {
			return errors.New("read authorization code failed")
		}
		challenge, err := client.Prepare(ctx, strings.TrimSpace(string(raw)))
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(challenge)
	case "commit":
		if err := client.Commit(ctx); err != nil {
			return err
		}
	case "sync":
		if err := client.Sync(ctx); err != nil {
			return err
		}
	case "status":
	default:
		return fmt.Errorf("unknown license command")
	}
	return json.NewEncoder(output).Encode(client.Status("installer"))
}
