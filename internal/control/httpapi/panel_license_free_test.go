package httpapi_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customeridentity"
	"github.com/hongle/hl-panel/internal/control/httpapi"
	"github.com/hongle/hl-panel/internal/control/panelquota"
	"github.com/hongle/hl-panel/internal/panellicense"
)

type unavailableLicenseUsage struct{}

func (unavailableLicenseUsage) PanelResourceUsage(context.Context) (panelquota.Usage, error) {
	return panelquota.Usage{}, errors.New("synthetic usage failure")
}

func TestFreeLicenseHTTPAdministratorBoundaryAndUsageFailure(t *testing.T) {
	f := newBusinessFixture(t)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	var calls atomic.Int32
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		op := map[string]string{panellicense.PublicPath + "/free/prepare": "free-prepare", panellicense.PublicPath + "/free/commit": "free-commit"}[r.URL.Path]
		var signed panellicense.InstanceRequest
		if err := json.NewDecoder(r.Body).Decode(&signed); err != nil {
			t.Error(err)
			http.Error(w, "invalid request", 400)
			return
		}
		req, err := panellicense.VerifyRequest(op, signed, now)
		if err != nil {
			t.Error(err)
			http.Error(w, "invalid signature", 401)
			return
		}
		key, err := base64.RawURLEncoding.DecodeString(req.InstancePublicKey)
		if err != nil {
			t.Error(err)
			return
		}
		thumbprint := panellicense.Thumbprint(ed25519.PublicKey(key))
		var value any
		switch op {
		case "free-prepare":
			value = panellicense.Challenge{Purpose: "free-registration", Version: panellicense.Version, Issuer: "https://issuer.example.test", Product: panellicense.Product, Audience: panellicense.Audience, RequestID: req.RequestID, Domain: req.Domain, InstanceID: req.InstanceID, InstanceKeyThumbprint: thumbprint, ReservationID: "isolated-free", DNSName: "_hl-license." + req.Domain, DNSValue: "hl-license-v2=isolated", IssuedAt: now, ExpiresAt: now.Add(10 * time.Minute)}
		case "free-commit":
			value = panellicense.Claims{Version: panellicense.Version, Issuer: "https://issuer.example.test", Product: panellicense.Product, Audience: panellicense.Audience, RequestID: req.RequestID, LicenseID: "lic-isolated-free", Domain: req.Domain, InstanceID: req.InstanceID, InstanceKeyThumbprint: thumbprint, Revision: 1, Status: "active", Tier: panellicense.TierFree, Limits: panellicense.Limits{DeviceGroups: 1, Rules: 10}, Term: panellicense.Term{Kind: "perpetual"}, ActivatedAt: now, IssuedAt: now, RefreshAfter: now.Add(30 * time.Minute), LeaseValidUntil: now.Add(24 * time.Hour)}
		default:
			t.Errorf("unexpected issuer operation: %s", r.URL.Path)
			http.Error(w, "unexpected", 404)
			return
		}
		envelope, err := panellicense.Sign(value, "isolated", private)
		if err != nil {
			t.Error(err)
			return
		}
		_ = json.NewEncoder(w).Encode(envelope)
	}))
	defer issuer.Close()
	client, err := panellicense.NewClient(panellicense.Config{IssuerURL: issuer.URL, IssuerID: "https://issuer.example.test", Keys: map[string]ed25519.PublicKey{"isolated": public}, Domain: "panel.example.test", InstanceID: "hlp-isolated-http", Directory: t.TempDir(), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	f.handler = panellicense.Guard(client, httpapi.New(auth.NewService(f.store, audit.NewService(f.store), time.Now, time.Hour), nil, nil, nil, nil, nil, nil, httpapi.WithPanelLicense(client), httpapi.WithPanelLicenseUsage(unavailableLicenseUsage{})))
	identity, err := customeridentity.NewService(f.store.CustomerIdentityRepository("test"), time.Now, customeridentity.Options{PasswordFingerprintKey: bytes.Repeat([]byte{1}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	customer, err := identity.Login(context.Background(), "demo-user", "12345678")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/v1/license/status"}, {"POST", "/api/v1/license/free/prepare"}, {"POST", "/api/v1/license/activation/prepare"}, {"POST", "/api/v1/license/activation/commit"}, {"POST", "/api/v1/license/sync"}, {"POST", "/api/v1/license/reminders/dismiss"},
	} {
		for _, token := range []string{"", customer.AccessToken} {
			requestJSON(t, f.handler, route.method, route.path, token, nil, 401)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unauthorized caller reached issuer")
	}
	// Missing claims must not contact the issuer merely to sync an identity.
	requestJSON(t, f.handler, "POST", "/api/v1/license/sync", f.token, nil, 502)
	if calls.Load() != 0 {
		t.Fatal("unbound sync reached issuer")
	}
	requestJSON(t, f.handler, "POST", "/api/v1/license/free/prepare", f.token, nil, 200)
	var activated struct {
		panellicense.Status
		Usage            *panelquota.Usage `json:"usage"`
		UsageUnavailable bool              `json:"usage_unavailable"`
	}
	decodeResponse(t, requestJSON(t, f.handler, "POST", "/api/v1/license/activation/commit", f.token, nil, 200), &activated)
	if !activated.ManagementAllowed || activated.Tier != "free" || !activated.UsageUnavailable || activated.Usage != nil {
		t.Fatalf("activation lost to usage failure: %+v", activated)
	}
	requestJSON(t, f.handler, "GET", "/api/v1/license/status", f.token, nil, 200)
	if calls.Load() != 2 {
		t.Fatalf("unexpected issuer calls: %d", calls.Load())
	}
}

func TestPanelQuotaHTTPConflictRetainsExistingGroups(t *testing.T) {
	f := newBusinessFixture(t)
	f.store.SetPanelQuotaProvider(func() panelquota.Limits { return panelquota.Limits{DeviceGroups: 1, Rules: 10} })
	before, err := f.store.PanelResourceUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	body := requestJSON(t, f.handler, "POST", "/api/v1/device-groups", f.token, map[string]any{"name": "over free cap", "kind": "ENTRY", "selection_policy": "weighted_round_robin"}, 409)
	var problem struct {
		Error struct{ Code, Message string }
	}
	decodeResponse(t, body, &problem)
	if problem.Error.Code != "panel_quota_exceeded" || problem.Error.Message == "" {
		t.Fatalf("wrong problem: %s", body)
	}
	after, err := f.store.PanelResourceUsage(context.Background())
	if err != nil || before != after {
		t.Fatalf("rejected create changed usage: %v %v %v", before, after, err)
	}
}
