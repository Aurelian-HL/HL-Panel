package httpapi_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/httpapi"
	p "github.com/hongle/hl-panel/internal/panellicense"
)

func TestShopHTTPActivationNeverRequiresForwardingServices(t *testing.T) {
	f := newBusinessFixture(t)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	var prepares, commits atomic.Int32
	var catalogFailure atomic.Bool
	var catalogStatus atomic.Int32
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		op := map[string]string{p.PublicPath + "/shop/catalog": "shop-catalog", p.PublicPath + "/redemption/prepare": "redeem-prepare", p.PublicPath + "/redemption/commit": "redeem-commit"}[r.URL.Path]
		var signed p.InstanceRequest
		if json.NewDecoder(r.Body).Decode(&signed) != nil {
			t.Error("could not decode instance request")
			w.WriteHeader(400)
			return
		}
		req, err := p.VerifyRequest(op, signed, now)
		if err != nil {
			t.Error("instance signature rejected", err)
			w.WriteHeader(401)
			return
		}
		key, _ := base64.RawURLEncoding.DecodeString(req.InstancePublicKey)
		thumb := p.Thumbprint(ed25519.PublicKey(key))
		var payload any
		switch op {
		case "shop-catalog":
			if status := catalogStatus.Load(); status != 0 {
				w.WriteHeader(int(status))
				return
			}
			if catalogFailure.Load() {
				w.WriteHeader(503)
				_, _ = w.Write([]byte("private issuer credential must not be exposed"))
				return
			}
			payload = p.ShopCatalog{Purpose: op, Version: p.Version, Issuer: "https://issuer.example.test", Product: p.Product, Audience: p.Audience, RequestID: req.RequestID, IssuedAt: now, ExpiresAt: now.Add(10 * time.Minute), Products: p.DefaultShopProducts()}
		case "redeem-prepare":
			prepares.Add(1)
			payload = p.RedemptionAttempt{Challenge: p.Challenge{Purpose: p.RedemptionPurpose, Version: p.Version, Issuer: "https://issuer.example.test", Product: p.Product, Audience: p.Audience, RequestID: req.RequestID, Domain: req.Domain, InstanceID: req.InstanceID, InstanceKeyThumbprint: thumb, ReservationID: "redeem_" + strings.Repeat("a", 32), VerificationMethod: "https-01", HTTPPath: p.InstallProofPath + "redeem_" + strings.Repeat("a", 32), HTTPValue: "hl-license-v2=" + strings.Repeat("b", 32), IssuedAt: now, ExpiresAt: now.Add(10 * time.Minute)}, CodeHash: strings.Repeat("0", 64)}
		case "redeem-commit":
			commits.Add(1)
			if req.Code != "" || req.Challenge == nil {
				t.Error("commit leaked code or lost receipt")
			}
			expiry := now.Add(30 * 24 * time.Hour)
			payload = p.Claims{Version: p.Version, Issuer: "https://issuer.example.test", Product: p.Product, Audience: p.Audience, RequestID: req.RequestID, LicenseID: "lic-shop-http", Domain: req.Domain, InstanceID: req.InstanceID, InstanceKeyThumbprint: thumb, Revision: 1, Status: "active", Tier: p.TierLicensed, Term: p.Term{Kind: "calendar_months", Months: 1}, ActivatedAt: now, LicenseExpiresAt: &expiry, IssuedAt: now, RefreshAfter: now.Add(30 * time.Minute), LeaseValidUntil: now.Add(24 * time.Hour)}
		default:
			t.Error("unexpected issuer route")
			w.WriteHeader(404)
			return
		}
		envelope, err := p.Sign(payload, "isolated", private)
		if err != nil {
			t.Error(err)
			return
		}
		_ = json.NewEncoder(w).Encode(envelope)
	}))
	defer issuer.Close()
	client, err := p.NewClient(p.Config{IssuerURL: issuer.URL, IssuerID: "https://issuer.example.test", Keys: map[string]ed25519.PublicKey{"isolated": public}, Domain: "panel.example.test", InstanceID: "hlp-shop-http", Directory: t.TempDir(), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately supply no forwarding, node or subscription services: the shop
	// must remain usable while restricted and cannot mutate these domains.
	f.handler = p.Guard(client, httpapi.New(auth.NewService(f.store, audit.NewService(f.store), time.Now, time.Hour), nil, nil, nil, nil, nil, nil, httpapi.WithPanelLicense(client)))
	// A pasted secret can accidentally become a JSON field name. Decoder errors
	// must never echo it, and invalid bodies must not reach the authority.
	secret := "hlr1_" + strings.Repeat("z", 32)
	for _, path := range []string{"/api/v1/license/redemption", "/api/v1/license/activation/prepare"} {
		for _, body := range []any{
			map[string]string{secret: "mistaken-field"},
			map[string]any{"code": map[string]string{secret: "invalid-type"}},
			map[string]string{"code": secret + strings.Repeat("x", 4096)},
		} {
			response := requestJSON(t, f.handler, "POST", path, f.token, body, 400)
			if strings.Contains(string(response), secret) {
				t.Fatal("license code was exposed by a request decoding error")
			}
		}
	}
	requestJSON(t, f.handler, "POST", "/api/v1/license/redemption", f.token, map[string]string{"code": "invalid"}, 409)
	if prepares.Load() != 0 || commits.Load() != 0 {
		t.Fatal("malformed code reached issuer")
	}
	var catalog struct {
		Items             []p.ShopProduct
		RedemptionEnabled bool `json:"redemption_enabled"`
	}
	decodeResponse(t, requestJSON(t, f.handler, "GET", "/api/v1/license/shop", f.token, nil, 200), &catalog)
	if len(catalog.Items) != 5 || !catalog.RedemptionEnabled {
		t.Fatal("catalog unavailable while unactivated")
	}
	var status p.Status
	decodeResponse(t, requestJSON(t, f.handler, "POST", "/api/v1/license/redemption", f.token, map[string]string{"code": "hlr1_" + strings.Repeat("a", 32)}, 200), &status)
	if !status.ManagementAllowed || status.Tier != p.TierLicensed || status.RedemptionPending || status.LicenseID != "lic-shop-http" {
		t.Fatal("redemption did not return durable active status")
	}
	requestJSON(t, f.handler, "POST", "/api/v1/license/redemption/resume", f.token, nil, 200)
	if prepares.Load() != 1 || commits.Load() != 1 {
		t.Fatal("completed activation was committed twice")
	}
	catalogFailure.Store(true)
	response := requestJSON(t, f.handler, "GET", "/api/v1/license/shop", f.token, nil, 502)
	if strings.Contains(string(response), "private issuer") {
		t.Fatal("raw upstream error was exposed")
	}
	if !client.Status("admin").ManagementAllowed {
		t.Fatal("catalog outage discarded existing authorization")
	}
	for _, status := range []int{404, 405, 501} {
		catalogStatus.Store(int32(status))
		decodeResponse(t, requestJSON(t, f.handler, "GET", "/api/v1/license/shop", f.token, nil, 200), &catalog)
		if len(catalog.Items) != 5 || catalog.RedemptionEnabled || !client.Status("admin").ManagementAllowed {
			t.Fatal("legacy issuer fallback must disable redemption without changing existing license")
		}
		for _, product := range catalog.Items {
			if product.PriceLabel != "" || product.PurchaseURL != "" {
				t.Fatal("legacy issuer fallback invented a purchase option")
			}
		}
	}
}
