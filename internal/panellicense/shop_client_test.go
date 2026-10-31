package panellicense

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type shopFixture struct {
	*signedFixture
	commits atomic.Int32
	catalog func(*ShopCatalog)
	attempt func(*RedemptionAttempt)
	commit  func(http.ResponseWriter, Request)
}

func newShopFixture(t *testing.T) *shopFixture {
	t.Helper()
	f := &shopFixture{signedFixture: newSignedFixture(t)}
	f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		op := map[string]string{PublicPath + "/shop/catalog": "shop-catalog", PublicPath + "/redemption/prepare": "redeem-prepare", PublicPath + "/redemption/commit": "redeem-commit"}[r.URL.Path]
		var signed InstanceRequest
		if json.NewDecoder(r.Body).Decode(&signed) != nil {
			t.Error("invalid signed request")
			w.WriteHeader(400)
			return
		}
		req, err := VerifyRequest(op, signed, f.now)
		if err != nil {
			t.Error("invalid request signature", err)
			w.WriteHeader(401)
			return
		}
		var payload any
		switch op {
		case "shop-catalog":
			catalog := ShopCatalog{Purpose: op, Version: Version, Issuer: f.client.config.IssuerID, Product: Product, Audience: Audience, RequestID: req.RequestID, IssuedAt: f.now, ExpiresAt: f.now.Add(10 * time.Minute), Products: DefaultShopProducts()}
			if f.catalog != nil {
				f.catalog(&catalog)
			}
			payload = catalog
		case "redeem-prepare":
			attempt := RedemptionAttempt{Challenge: Challenge{Purpose: RedemptionPurpose, Version: Version, Issuer: f.client.config.IssuerID, Product: Product, Audience: Audience, RequestID: req.RequestID, Domain: req.Domain, InstanceID: req.InstanceID, InstanceKeyThumbprint: Thumbprint(f.client.key.Public().(ed25519.PublicKey)), ReservationID: "redeem_" + strings.Repeat("a", 32), VerificationMethod: "https-01", HTTPPath: InstallProofPath + "redeem_" + strings.Repeat("a", 32), HTTPValue: "hl-license-v2=" + strings.Repeat("b", 32), IssuedAt: f.now, ExpiresAt: f.now.Add(10 * time.Minute)}, CodeHash: strings.Repeat("0", 64)}
			if f.attempt != nil {
				f.attempt(&attempt)
			}
			payload = attempt
		case "redeem-commit":
			f.commits.Add(1)
			if req.Code != "" || req.Challenge == nil {
				t.Error("commit must use only saved signed receipt")
			}
			if f.commit != nil {
				f.commit(w, req)
				return
			}
			payload = f.claims(req.RequestID)
		default:
			t.Error("unexpected operation")
			w.WriteHeader(404)
			return
		}
		envelope, err := Sign(payload, "test", f.key)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		_ = json.NewEncoder(w).Encode(envelope)
	})
	return f
}

func TestShopCatalogRequiresSignedCurrentRequestAndSafeProducts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ShopCatalog)
	}{
		{"valid", nil},
		{"purpose", func(c *ShopCatalog) { c.Purpose = "license" }},
		{"nonce", func(c *ShopCatalog) { c.RequestID = "another-request" }},
		{"audience", func(c *ShopCatalog) { c.Audience = "another-product" }},
		{"issuer", func(c *ShopCatalog) { c.Issuer = "https://another.example.test" }},
		{"expired", func(c *ShopCatalog) {
			c.IssuedAt = c.IssuedAt.Add(-20 * time.Minute)
			c.ExpiresAt = c.IssuedAt.Add(10 * time.Minute)
		}},
		{"future", func(c *ShopCatalog) {
			c.IssuedAt = c.IssuedAt.Add(time.Hour)
			c.ExpiresAt = c.IssuedAt.Add(10 * time.Minute)
		}},
		{"long-lived", func(c *ShopCatalog) { c.ExpiresAt = c.ExpiresAt.Add(time.Minute) }},
		{"unsafe-link", func(c *ShopCatalog) { c.Products[0].PurchaseURL = "javascript:alert(1)" }},
		{"duplicate-product", func(c *ShopCatalog) { c.Products = append(c.Products, c.Products[0]) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newShopFixture(t)
			f.catalog = tc.change
			items, err := f.client.ShopCatalog(context.Background())
			if tc.change == nil {
				if err != nil || len(items) != 5 || items[0].PurchaseURL != "" || items[0].PriceLabel != "" {
					t.Fatal("default catalog invented price or purchase link", err)
				}
			} else if err == nil {
				t.Fatal("invalid signed catalog accepted")
			}
		})
	}
}

func TestShopNotOpenOnlyForUnsupportedCatalogEndpoint(t *testing.T) {
	for _, status := range []int{404, 405, 501, 401, 403, 500, 503} {
		f := newShopFixture(t)
		f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte("untrusted upstream response"))
		})
		_, err := f.client.ShopCatalog(context.Background())
		want := status == 404 || status == 405 || status == 501
		if err == nil || errors.Is(err, ErrShopNotOpen) != want {
			t.Fatalf("catalog status %d: %v", status, err)
		}
		if err = f.client.Redeem(context.Background(), "hlr1_"+strings.Repeat("a", 32)); err == nil || errors.Is(err, ErrShopNotOpen) {
			t.Fatalf("redemption must not interpret unsupported endpoint as a catalog: %v", err)
		}
	}
}

func TestRedemptionReceiptMustBeDurableBeforeCommit(t *testing.T) {
	f := newShopFixture(t)
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	f.client.config.Directory = blocked
	err := f.client.Redeem(context.Background(), "hlr1_"+strings.Repeat("a", 32))
	var safe *UserError
	if !errors.As(err, &safe) || safe.Code != "redemption_save_failed" || strings.Contains(err.Error(), blocked) || f.commits.Load() != 0 || f.client.Status("admin").RedemptionPending {
		t.Fatal("non-durable receipt committed or exposed path", err)
	}
}

func TestRedemptionInvalidReceiptNeverCommits(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*RedemptionAttempt)
	}{
		{"nonce", func(a *RedemptionAttempt) { a.RequestID = "other-request" }},
		{"instance", func(a *RedemptionAttempt) { a.InstanceID = "another-instance" }},
		{"method", func(a *RedemptionAttempt) { a.VerificationMethod = "http-01" }},
		{"path", func(a *RedemptionAttempt) { a.HTTPPath = "/admin" }},
		{"expired", func(a *RedemptionAttempt) { a.ExpiresAt = a.IssuedAt.Add(-time.Second) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newShopFixture(t)
			f.attempt = tc.change
			if err := f.client.Redeem(context.Background(), "hlr1_"+strings.Repeat("a", 32)); err == nil || f.commits.Load() != 0 || f.client.Status("").RedemptionPending {
				t.Fatal("invalid receipt committed or saved")
			}
		})
	}
}

func TestRedemptionResultStorageFailurePreservesOldRightsAndReceipt(t *testing.T) {
	f := newShopFixture(t)
	if err := f.accept(f.claims("initial")); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(f.client.config.Directory, "state.json")
	savedPath := filepath.Join(f.client.config.Directory, "pending-test.json")
	f.commit = func(w http.ResponseWriter, r Request) {
		// Deliberately make only the final state replacement fail, after the
		// pending receipt has already been persisted by the client.
		if f.commits.Load() == 1 {
			if err := os.Rename(statePath, savedPath); err != nil {
				t.Error(err)
			}
			if err := os.Mkdir(statePath, 0700); err != nil {
				t.Error(err)
			}
		}
		s := f.claims(r.RequestID)
		s.Revision = 2
		expiry := f.expires.Add(30 * 24 * time.Hour)
		s.LicenseExpiresAt = &expiry
		e, err := Sign(s, "test", f.key)
		if err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(e)
	}
	if err := f.client.Redeem(context.Background(), "hlr1_"+strings.Repeat("a", 32)); err == nil {
		t.Fatal("failed persistence reported success")
	}
	if s := f.client.Status("admin"); s.Revision != 1 || !s.ManagementAllowed || !s.RedemptionPending {
		t.Fatal("failed persistence discarded previous rights or receipt")
	}
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(savedPath, statePath); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewClient(f.client.config)
	if err != nil || !restarted.Status("").RedemptionPending {
		t.Fatal("saved receipt not restartable", err)
	}
	if err := restarted.ResumeRedemption(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := restarted.Status("admin"); s.Revision != 2 || s.RedemptionPending || !s.ManagementAllowed {
		t.Fatal("could not recover durable authorization")
	}
	if err := restarted.ResumeRedemption(context.Background()); err != nil || f.commits.Load() != 2 {
		t.Fatal("completed receipt resubmitted")
	}
}

func TestRedemptionProofRouteAndTerminalFailure(t *testing.T) {
	f := newShopFixture(t)
	f.commit = func(w http.ResponseWriter, r Request) {
		w.WriteHeader(409)
		var body ErrorResponse
		body.Error.Code = "https_not_verified"
		_ = json.NewEncoder(w).Encode(body)
	}
	if err := f.client.Redeem(context.Background(), "hlr1_"+strings.Repeat("a", 32)); err == nil || !f.client.Status("").RedemptionPending {
		t.Fatal("recoverable proof failure lost receipt")
	}
	proof := *f.client.redemption
	for _, tc := range []struct {
		method, host, path string
		status             int
	}{
		{http.MethodGet, f.client.config.Domain, proof.HTTPPath, 200},
		{http.MethodPost, f.client.config.Domain, proof.HTTPPath, 404},
		{http.MethodGet, "another.example.test", proof.HTTPPath, 404},
		{http.MethodGet, f.client.config.Domain, proof.HTTPPath + "?x=1", 404},
		{http.MethodGet, f.client.config.Domain, proof.HTTPPath + "bad", 404},
	} {
		w := httptest.NewRecorder()
		f.client.ServeProof(w, httptest.NewRequest(tc.method, "https://"+tc.host+tc.path, nil))
		if w.Code != tc.status || (w.Code == 200 && w.Body.String() != proof.HTTPValue) || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("incorrect proof route boundary", tc.status, w.Code)
		}
	}
	f.commit = func(w http.ResponseWriter, r Request) {
		w.WriteHeader(409)
		var body ErrorResponse
		body.Error.Code = "code_disabled"
		_ = json.NewEncoder(w).Encode(body)
	}
	if err := f.client.ResumeRedemption(context.Background()); err == nil || f.client.Status("").RedemptionPending {
		t.Fatal("terminal refusal left unusable receipt")
	}
}
