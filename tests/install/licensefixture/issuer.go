package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	p "github.com/hongle/hl-panel/internal/panellicense"
	"github.com/hongle/hl-panel/internal/panellicenseissuer"
)

// Only this unshipped, isolation-gated test executable can direct HTTP-01 to
// loopback. It still traverses the installed Nginx and production API handler.
type localProof struct{}

func (localProof) VerifyHTTPProof(ctx context.Context, domain, path, value string) error {
	if domain != "hl-panel-smoke.invalid" || !strings.HasPrefix(path, p.InstallProofPath) {
		return errors.New("unexpected fixture domain or path")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1"+path, nil)
	if err != nil {
		return err
	}
	request.Host = domain
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1024))
	if err != nil || response.StatusCode != http.StatusOK || string(body) != value {
		return errors.New("HTTP proof not served by installed instance")
	}
	return nil
}

func issuerFixture(directory, dsnFile string, serve bool) error {
	if !filepath.IsAbs(dsnFile) {
		return errors.New("absolute disposable DSN file required")
	}
	raw, err := os.ReadFile(dsnFile)
	if err != nil {
		return err
	}
	// The fixture may never select a production database or a remote host.
	dsn := strings.TrimSpace(string(raw))
	if dsn != "postgres:///hl_license_install_fixture?host=/var/run/postgresql&user=root" {
		return errors.New("unexpected disposable DSN")
	}
	store, err := panellicenseissuer.OpenPostgres(context.Background(), dsn)
	if err != nil {
		return err
	}
	defer store.Close()
	if !serve {
		records, err := store.List(context.Background())
		if err != nil {
			return err
		}
		if len(records) != 1 {
			return errors.New("expected exactly one durable free registration")
		}
		item := records[0]
		if item.Domain != "hl-panel-smoke.invalid" || item.Tier != p.TierFree || item.Limits != (p.Limits{DeviceGroups: 1, Rules: 10}) || item.Term.Kind != "perpetual" {
			return errors.New("incorrect free registration")
		}
		audit, err := store.Audit(context.Background(), item.ID)
		if err != nil {
			return err
		}
		if len(audit) == 0 {
			return errors.New("missing registration audit")
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"free_registration": item, "audit_events": len(audit)})
	}
	key, err := os.ReadFile(filepath.Join(directory, "issuer-key"))
	if err != nil {
		return err
	}
	service, err := panellicenseissuer.New(store, panellicenseissuer.Config{Issuer: issuer, KeyID: keyID, SigningKey: ed25519.PrivateKey(key), AdminToken: strings.Repeat("isolated-fixture-only-", 3), HTTPVerifier: localProof{}})
	if err != nil {
		return err
	}
	server := &http.Server{Addr: "127.0.0.1:18712", Handler: service.Handler(), ReadHeaderTimeout: 5 * time.Second}
	return server.ListenAndServe()
}
