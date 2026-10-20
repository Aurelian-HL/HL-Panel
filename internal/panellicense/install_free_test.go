package panellicense

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func installFixture(t *testing.T) (*signedFixture, *bool, *int) {
	t.Helper()
	f := newSignedFixture(t)
	f.client.config.AutoRegisterFree = true
	reachable := false
	prepares := 0
	endpoint := httptest.NewServer(Guard(f.client, http.NotFoundHandler()))
	t.Cleanup(endpoint.Close)
	f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		op := map[string]string{PublicPath + "/free/install/prepare": "free-install-prepare", PublicPath + "/free/install/commit": "free-install-commit"}[r.URL.Path]
		var signed InstanceRequest
		if err := json.NewDecoder(r.Body).Decode(&signed); err != nil {
			t.Error(err)
			http.Error(w, "bad", 400)
			return
		}
		req, err := VerifyRequest(op, signed, f.now)
		if err != nil {
			t.Error(err)
			http.Error(w, "signature", 401)
			return
		}
		if req.Code != "" || req.ReservationID != "" || req.LicenseID != "" {
			t.Error("automatic flow sent paid fields")
		}
		var value any
		if op == "free-install-prepare" {
			prepares++
			nonce := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("a", 24)))
			proof := Challenge{Version: Version, Issuer: f.client.config.IssuerID, Product: Product, Audience: Audience, RequestID: req.RequestID, Domain: req.Domain, InstanceID: req.InstanceID, InstanceKeyThumbprint: Thumbprint(f.client.key.Public().(ed25519.PublicKey)), Purpose: InstallFreePurpose, VerificationMethod: "http-01", ReservationID: "free_" + nonce, HTTPPath: InstallProofPath + "free_" + nonce, HTTPValue: "hl-license-v2=" + nonce, IssuedAt: f.now, ExpiresAt: f.now.Add(10 * time.Minute)}
			value = proof
		} else if op == "free-install-commit" {
			var proof Challenge
			if req.Challenge == nil || Verify(*req.Challenge, f.client.config.Keys, &proof) != nil || !ValidInstallProof(proof) {
				t.Error("missing complete signed challenge")
				http.Error(w, "proof", 400)
				return
			}
			if !reachable {
				http.Error(w, "HTTP not reachable", 409)
				return
			}
			callback, _ := http.NewRequest(http.MethodGet, endpoint.URL+proof.HTTPPath, nil)
			callback.Host = req.Domain
			response, err := endpoint.Client().Do(callback)
			if err != nil {
				t.Error(err)
				return
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != 200 || string(body) != proof.HTTPValue {
				t.Errorf("real callback failed: %d %s", response.StatusCode, body)
				http.Error(w, "proof", 409)
				return
			}
			claims := f.claims(req.RequestID)
			claims.Tier = TierFree
			claims.Limits = freeClientLimits()
			claims.Term = Term{Kind: "perpetual"}
			claims.LicenseExpiresAt = nil
			value = claims
		} else {
			t.Error("unexpected route")
			http.NotFound(w, r)
			return
		}
		envelope, _ := Sign(value, "test", f.key)
		_ = json.NewEncoder(w).Encode(envelope)
	})
	return f, &reachable, &prepares
}

func TestAutomaticInstallHTTPCallbackRetryAndRestart(t *testing.T) {
	f, reachable, prepares := installFixture(t)
	if !f.client.Status("").AutoRegistration {
		t.Fatal("new installation not pending")
	}
	if err := f.client.reconcile(context.Background()); err == nil {
		t.Fatal("unreachable proof accepted")
	}
	if f.client.Status("").ManagementAllowed || !strings.Contains(f.client.Status("").LastError, "自动重试") {
		t.Fatal("pending state incorrect")
	}
	// Validate every request boundary on the actual guard handler.
	proof := *f.client.challenge
	for _, tc := range []struct {
		method, path, host string
		want               int
	}{
		{"GET", proof.HTTPPath, f.client.config.Domain, 200},
		{"POST", proof.HTTPPath, f.client.config.Domain, 404},
		{"GET", proof.HTTPPath, "other.example.test", 404},
		{"GET", proof.HTTPPath + "?x=1", f.client.config.Domain, 404},
		{"GET", proof.HTTPPath + "wrong", f.client.config.Domain, 404},
	} {
		req := httptest.NewRequest(tc.method, "http://"+tc.host+tc.path, nil)
		w := httptest.NewRecorder()
		Guard(f.client, http.NotFoundHandler()).ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Fatalf("%s %s %s: %d", tc.method, tc.path, tc.host, w.Code)
		}
		if w.Code == 200 && (w.Body.String() != proof.HTTPValue || strings.Contains(w.Body.String(), "signature")) {
			t.Fatal("proof leaked data")
		}
	}
	reloaded, err := NewClient(f.client.config)
	if err != nil {
		t.Fatal(err)
	}
	// The new handler uses the restored challenge and same key, without prepare.
	f.client = reloaded
	*reachable = true
	endpoint := httptest.NewServer(Guard(reloaded, http.NotFoundHandler()))
	defer endpoint.Close()
	// installFixture's callback handler remains attached to the original client;
	// the same restored proof must also be served by the restarted instance.
	req, _ := http.NewRequest("GET", endpoint.URL+proof.HTTPPath, nil)
	req.Host = reloaded.config.Domain
	resp, err := endpoint.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("restored proof missing")
	}
	if err := reloaded.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := reloaded.Status("")
	if !s.ManagementAllowed || s.AutoRegistration || s.Tier != TierFree || *s.Limits != freeClientLimits() || *prepares != 1 {
		t.Fatalf("registered status: %+v prepares=%d", s, *prepares)
	}
	again, err := NewClient(reloaded.config)
	if err != nil || !again.Status("").ManagementAllowed || !again.key.Equal(reloaded.key) {
		t.Fatal("activation identity not durable", err)
	}
}

func TestAutomaticInstallExpiryAndPaidFlowIsolation(t *testing.T) {
	f, _, prepares := installFixture(t)
	_ = f.client.reconcile(context.Background())
	proof := *f.client.challenge
	f.now = f.now.Add(11 * time.Minute)
	req := httptest.NewRequest("GET", "http://"+f.client.config.Domain+proof.HTTPPath, nil)
	w := httptest.NewRecorder()
	f.client.ServeInstallProof(w, req)
	if w.Code != 404 {
		t.Fatal("expired proof served")
	}
	_ = f.client.reconcile(context.Background())
	if *prepares != 2 {
		t.Fatal("expired proof not refreshed")
	}
	// A manual paid prepare supersedes auto registration; background cannot commit it.
	f.server.Config.Handler = http.HandlerFunc(f.serve)
	if _, err := f.client.Prepare(context.Background(), "hl-test-secret-code"); err != nil {
		t.Fatal(err)
	}
	if f.client.Status("").AutoRegistration {
		t.Fatal("paid flow still automatic")
	}
	before := f.calls.Load()
	if err := f.client.commit(context.Background(), true); err == nil || f.calls.Load() != before {
		t.Fatal("background committed paid challenge")
	}
	if err := f.client.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.now = f.expires.Add(time.Second)
	if err := f.client.ReconcileInstallation(context.Background()); err == nil {
		t.Fatal("expired paid license downgraded")
	}
}

func TestAutomaticInstallDisabledAndCorruptStateNeverEnroll(t *testing.T) {
	f := newSignedFixture(t)
	if err := f.client.ReconcileInstallation(context.Background()); err == nil {
		t.Fatal("missing installer intent accepted")
	}
	if err := os.WriteFile(filepath.Join(f.client.config.Directory, "state.json"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	f.client.config.AutoRegisterFree = true
	client, err := NewClient(f.client.config)
	if err != nil {
		t.Fatal(err)
	}
	if client.Status("").AutoRegistration || client.ReconcileInstallation(context.Background()) == nil || f.calls.Load() != 0 {
		t.Fatal("corrupt signed cache silently enrolled")
	}
}

func TestInstallProofContractRejectsUnsafeValues(t *testing.T) {
	f, _, _ := installFixture(t)
	_ = f.client.reconcile(context.Background())
	valid := *f.client.challenge
	for _, change := range []func(*Challenge){
		func(c *Challenge) { c.HTTPPath = "http://other.example.test/" },
		func(c *Challenge) { c.DNSName = "_hl-license." + c.Domain },
		func(c *Challenge) { c.HTTPValue = "hl-license-v2=short" },
		func(c *Challenge) { c.ReservationID += "/../" },
		func(c *Challenge) { c.VerificationMethod = "dns-01" },
	} {
		proof := valid
		change(&proof)
		if ValidInstallProof(proof) {
			t.Fatal("unsafe proof accepted")
		}
	}
}
