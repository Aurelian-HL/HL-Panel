// Command licensefixture creates disposable CI trust and signed offline caches.
// It is never included in the official release or accepted by its pinned keys.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hongle/hl-panel/internal/panellicense"
)

const issuer = "https://hl-panel-isolated-ci.invalid"
const keyID = "disposable-ci-only"

func write(path string, value []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(value); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
func run() error {
	if os.Getenv("HL_PANEL_DISPOSABLE_INSTALL_TEST") != "true" && os.Getenv("HL_PANEL_DISPOSABLE_MIGRATION_TEST") != "true" {
		return fmt.Errorf("explicit disposable test isolation is required")
	}
	flags := flag.NewFlagSet("licensefixture", flag.ContinueOnError)
	directory := flags.String("fixture", "", "")
	state := flags.String("state", "", "")
	domain := flags.String("domain", "", "")
	serve := flags.Bool("serve", false, "run disposable real issuer")
	check := flags.Bool("check", false, "verify durable free registration")
	dsnFile := flags.String("dsn-file", "", "disposable database connection file")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return err
	}
	if !filepath.IsAbs(*directory) {
		return fmt.Errorf("absolute fixture directory is required")
	}
	if *serve || *check {
		return issuerFixture(*directory, *dsnFile, *serve)
	}
	if *state == "" {
		if err := os.Mkdir(*directory, 0700); err != nil {
			return err
		}
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		if err := write(filepath.Join(*directory, "issuer-key"), private); err != nil {
			return err
		}
		keys, _ := json.Marshal(map[string]string{keyID: base64.RawURLEncoding.EncodeToString(public)})
		linker := "-X main.panelLicenseIssuerURL=http://127.0.0.1:18712 -X main.panelLicenseIssuerID=" + issuer + " -X main.panelLicensePublicKeysB64=" + base64.RawURLEncoding.EncodeToString(keys)
		return write(filepath.Join(*directory, "ldflags"), []byte(linker))
	}
	if !filepath.IsAbs(*state) {
		return fmt.Errorf("absolute state directory is required")
	}
	name, err := panellicense.NormalizeDomain(*domain)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(*directory, "issuer-key"))
	if err != nil {
		return err
	}
	if len(raw) != ed25519.PrivateKeySize {
		return fmt.Errorf("invalid disposable issuer key")
	}
	if err := os.Mkdir(*state, 0700); err != nil {
		return err
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	id := "ci-" + hex.EncodeToString(nonce[:])
	now := time.Now().UTC()
	expires := now.Add(48 * time.Hour)
	claims := panellicense.Claims{Version: 2, Tier: panellicense.TierLicensed, Limits: panellicense.Limits{}, Issuer: issuer, Product: panellicense.Product, Audience: panellicense.Audience, RequestID: hex.EncodeToString(nonce[:]), LicenseID: "ci-license-" + id, Domain: name, InstanceID: id, InstanceKeyThumbprint: panellicense.Thumbprint(public), Revision: 1, Status: "active", Term: panellicense.Term{Kind: "days", Days: 2}, ActivatedAt: now, LicenseExpiresAt: &expires, IssuedAt: now, RefreshAfter: now.Add(23 * time.Hour), LeaseValidUntil: now.Add(24 * time.Hour)}
	envelope, err := panellicense.Sign(claims, keyID, ed25519.PrivateKey(raw))
	if err != nil {
		return err
	}
	cache, _ := json.Marshal(map[string]any{"envelope": envelope})
	identity, _ := json.Marshal(map[string]string{"domain": name, "instance_id": id})
	for name, value := range map[string][]byte{"instance-id": []byte(id + "\n"), "instance-key": private, "state.json": cache, "identity.json": identity} {
		if err := write(filepath.Join(*state, name), value); err != nil {
			return err
		}
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
