package panellicense

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
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

type signedFixture struct {
	t        *testing.T
	now      time.Time
	key      ed25519.PrivateKey
	client   *Client
	server   *httptest.Server
	expires  time.Time
	revision int64
	status   string
	fail     bool
	calls    atomic.Int32
}

func newSignedFixture(t *testing.T) *signedFixture {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &signedFixture{t: t, now: time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC), key: key, revision: 1, status: "active"}
	f.expires = f.now.Add(48 * time.Hour)
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	f.client, err = NewClient(Config{IssuerURL: f.server.URL, IssuerID: "https://issuer.example.test", Keys: map[string]ed25519.PublicKey{"test": pub}, Domain: "panel.example.test", InstanceID: "hlp-test-instance", Directory: t.TempDir(), Now: func() time.Time { return f.now }})
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *signedFixture) claims(nonce string) Claims {
	lease := f.now.Add(24 * time.Hour)
	if f.status == "active" && f.expires.Before(lease) {
		lease = f.expires
	}
	refresh := f.now.Add(30 * time.Minute)
	if !refresh.Before(lease) {
		refresh = f.now.Add(lease.Sub(f.now) / 2)
	}
	return Claims{Version: Version, Tier: TierLicensed, Limits: Limits{}, Issuer: f.client.config.IssuerID, Product: Product, Audience: Audience, RequestID: nonce, Domain: f.client.config.Domain, InstanceID: f.client.config.InstanceID, InstanceKeyThumbprint: Thumbprint(f.client.key.Public().(ed25519.PublicKey)), LicenseID: "lic-test", Revision: f.revision, Status: f.status, Term: Term{Kind: "days", Days: 2}, ActivatedAt: time.Date(2026, 10, 20, 7, 0, 0, 0, time.UTC), LicenseExpiresAt: &f.expires, IssuedAt: f.now, RefreshAfter: refresh, LeaseValidUntil: lease}
}
func (f *signedFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.calls.Add(1)
	if f.fail {
		http.Error(w, "down", 503)
		return
	}
	var signed InstanceRequest
	if err := json.NewDecoder(r.Body).Decode(&signed); err != nil {
		http.Error(w, "bad", 400)
		return
	}
	op := map[string]string{PublicPath + "/activation/prepare": "prepare", PublicPath + "/activation/commit": "commit", PublicPath + "/sync": "sync"}[r.URL.Path]
	req, err := VerifyRequest(op, signed, f.now)
	if err != nil {
		http.Error(w, "signature", 401)
		return
	}
	var value any = f.claims(req.RequestID)
	if op == "prepare" {
		if req.Code != "hl-test-secret-code" {
			http.Error(w, "code", 401)
			return
		}
		value = Challenge{Version: Version, Issuer: f.client.config.IssuerID, Product: Product, Audience: Audience, RequestID: req.RequestID, Domain: req.Domain, InstanceID: req.InstanceID, InstanceKeyThumbprint: Thumbprint(f.client.key.Public().(ed25519.PublicKey)), ReservationID: "res-test", DNSName: "_hl-license." + req.Domain, DNSValue: "hl-license-v2=proof", IssuedAt: f.now, ExpiresAt: f.now.Add(10 * time.Minute)}
	} else if op == "commit" && req.ReservationID != "res-test" {
		http.Error(w, "reservation", 401)
		return
	}
	envelope, err := Sign(value, "test", f.key)
	if err != nil {
		panic(err)
	}
	_ = json.NewEncoder(w).Encode(envelope)
}
func (f *signedFixture) activate() {
	f.t.Helper()
	challenge, err := f.client.Prepare(context.Background(), "hl-test-secret-code")
	if err != nil || challenge.DNSName != "_hl-license.panel.example.test" {
		f.t.Fatalf("prepare: %+v %v", challenge, err)
	}
	if err = f.client.Commit(context.Background()); err != nil {
		f.t.Fatal(err)
	}
}
func (f *signedFixture) accept(s Claims) error {
	envelope, err := Sign(s, "test", f.key)
	if err != nil {
		return err
	}
	return f.client.accept(envelope, s.RequestID)
}

func TestActivationExpiryRenewalPersistenceAndReminder(t *testing.T) {
	f := newSignedFixture(t)
	if f.client.Status("admin").ManagementAllowed {
		t.Fatal("unactivated client permits writes")
	}
	f.activate()
	first := f.client.Status("admin")
	if !first.ManagementAllowed || !first.ReminderDue {
		t.Fatalf("active: %+v", first)
	}
	if err := f.client.Dismiss("admin"); err != nil {
		t.Fatal(err)
	}
	if f.client.Status("admin").ReminderDue || !f.client.Status("other-admin").ReminderDue {
		t.Fatal("dismissal leaked across administrators")
	}
	reloaded, err := NewClient(f.client.config)
	if err != nil || reloaded.Status("admin").ReminderDue || !reloaded.Status("admin").ManagementAllowed {
		t.Fatalf("reload: %v", err)
	}
	if !f.client.key.Equal(reloaded.key) {
		t.Fatal("instance identity changed on restart")
	}
	f.now = f.expires.Add(time.Second)
	if f.client.Status("admin").Status != "expired" {
		t.Fatal("commercial expiry ignored offline")
	}
	f.status = "expired"
	if err = f.client.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.expires = f.now.Add(30 * 24 * time.Hour)
	f.status = "active"
	f.revision++
	if err = f.client.Sync(context.Background()); !errors.Is(err, ErrSyncThrottled) {
		t.Fatalf("throttled request claimed success: %v", err)
	}
	f.now = f.now.Add(11 * time.Second)
	if err = f.client.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := f.client.Status("admin")
	if !s.ManagementAllowed || s.LicenseID != first.LicenseID || s.Revision != 2 {
		t.Fatalf("renewal did not restore same license: %+v", s)
	}
	f.now = f.now.Add(11 * time.Second)
	f.status = "revoked"
	f.revision++
	if err = f.client.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s = f.client.Status("admin"); s.ManagementAllowed || s.ReminderDue || s.Status != "revoked" {
		t.Fatalf("revocation: %+v", s)
	}
	data, err := os.ReadFile(filepath.Join(f.client.config.Directory, "state.json"))
	if err != nil || strings.Contains(string(data), "hl-test-secret-code") {
		t.Fatal("authorization code persisted")
	}
}

func TestSignedResponsesRejectSubstitutionRollbackAndConflictingRevision(t *testing.T) {
	f := newSignedFixture(t)
	f.activate()
	base := f.claims("nonce")
	cases := map[string]func(*Claims){
		"domain":           func(s *Claims) { s.Domain = "other.example.test" },
		"instance":         func(s *Claims) { s.InstanceID = "other-instance" },
		"key":              func(s *Claims) { s.InstanceKeyThumbprint = "other" },
		"issuer":           func(s *Claims) { s.Issuer = "https://other.test" },
		"audience":         func(s *Claims) { s.Audience = "other" },
		"future":           func(s *Claims) { s.IssuedAt = f.now.Add(6 * time.Minute) },
		"older":            func(s *Claims) { s.IssuedAt = f.now.Add(-time.Second) },
		"revision":         func(s *Claims) { s.Revision = 0 },
		"conflict":         func(s *Claims) { x := f.expires.Add(time.Hour); s.LicenseExpiresAt = &x },
		"commercial-lease": func(s *Claims) { x := f.now.Add(time.Hour); s.LicenseExpiresAt = &x },
		"perpetual-unbounded": func(s *Claims) {
			s.Term = Term{Kind: "perpetual"}
			s.LicenseExpiresAt = nil
			s.LeaseValidUntil = f.now.Add(25 * time.Hour)
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s := base
			change(&s)
			if err := f.accept(s); err == nil {
				t.Fatal("invalid response accepted")
			}
			if !f.client.Status("admin").ManagementAllowed {
				t.Fatal("rejection destroyed valid cache")
			}
		})
	}
	env, _ := Sign(base, "test", f.key)
	if err := f.client.accept(env, "different-nonce"); err == nil {
		t.Fatal("response nonce not checked")
	}
	env.Payload = base64.RawURLEncoding.EncodeToString([]byte(`{"status":"active"}`))
	if err := f.client.accept(env, "nonce"); err == nil {
		t.Fatal("tampered signature accepted")
	}
}

func TestOfflineLeaseAndCorruptCacheFailClosedWithoutStoppingService(t *testing.T) {
	f := newSignedFixture(t)
	f.activate()
	f.fail = true
	if err := f.client.Sync(context.Background()); err == nil {
		t.Fatal("offline sync succeeded")
	}
	if !f.client.Status("admin").ManagementAllowed {
		t.Fatal("temporary offline discarded lease")
	}
	f.now = f.now.Add(25 * time.Hour)
	if f.client.Status("admin").Status != "stale" {
		t.Fatal("offline lease is unbounded")
	}
	path := filepath.Join(f.client.config.Directory, "state.json")
	if err := os.WriteFile(path, []byte(`{"envelope":{"payload":"forged"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(f.client.config)
	if err != nil {
		t.Fatalf("corrupt license prevented control service startup: %v", err)
	}
	if s := client.Status("admin"); s.ManagementAllowed || s.LastError == "" {
		t.Fatalf("forged state not restricted: %+v", s)
	}
	if raw, _ := os.ReadFile(path); !strings.Contains(string(raw), "forged") {
		t.Fatal("diagnostic evidence overwritten during load")
	}
}

func TestPerpetualStillRequiresSignedLimitedLease(t *testing.T) {
	f := newSignedFixture(t)
	s := f.claims("perpetual")
	s.Term = Term{Kind: "perpetual"}
	s.LicenseExpiresAt = nil
	if err := f.accept(s); err != nil {
		t.Fatal(err)
	}
	if st := f.client.Status("admin"); !st.ManagementAllowed || st.ReminderDue || st.ExpiresAt != nil {
		t.Fatalf("perpetual: %+v", st)
	}
	f.now = f.now.Add(25 * time.Hour)
	if f.client.Status("admin").ManagementAllowed {
		t.Fatal("perpetual bypassed sync lease")
	}
}

func TestReminderUsesShanghaiDay(t *testing.T) {
	f := newSignedFixture(t)
	f.now = time.Date(2026, 10, 20, 15, 59, 0, 0, time.UTC)
	f.activate()
	if err := f.client.Dismiss("admin"); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(2 * time.Minute)
	if !f.client.Status("admin").ReminderDue {
		t.Fatal("Shanghai midnight did not reset reminder")
	}
}

func TestNoCodeSentToRedirectAndOnlyHTTPSOrLiteralLoopback(t *testing.T) {
	f := newSignedFixture(t)
	var leaked atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer sink.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	f.client.config.IssuerURL = redirect.URL
	if _, err := f.client.Prepare(context.Background(), "hl-test-secret-code"); err == nil {
		t.Fatal("redirect accepted")
	}
	if leaked.Load() != 0 {
		t.Fatal("code or signature leaked to redirect target")
	}
	for _, u := range []string{"http://localhost:8080", "http://issuer.example.test", "https://user:pass@example.test", "https://issuer.example.test/path"} {
		c := f.client.config
		c.IssuerURL = u
		if _, err := NewClient(c); err == nil {
			t.Fatalf("unsafe issuer accepted: %s", u)
		}
	}
}

func TestExpiredGuardPreservesRuntimeAndRejectsBusinessMutations(t *testing.T) {
	f := newSignedFixture(t)
	handler := Guard(f.client, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer valid" {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(204)
	}))
	for _, tc := range []struct {
		method, path string
		allowed      bool
	}{
		{"GET", "/api/v1/public/subscriptions/stable-token", true},
		{"GET", "/api/v1/agent/desired", true}, {"POST", "/api/v1/agent/heartbeat", true},
		{"POST", "/api/v1/usage/reports", true}, {"POST", "/api/v1/agent/apply-results", true},
		{"POST", "/api/v1/license/sync", true}, {"PUT", "/api/v1/auth/password", true},
		{"POST", "/api/v1/forward-rules", false}, {"PUT", "/api/v1/forward-rules/existing", false},
		{"DELETE", "/api/v1/customers/existing", false}, {"POST", "/api/v1/agent/enroll", false},
		{"POST", "/api/v1/agent/update-task/claim", false},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set("Authorization", "Bearer valid")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		want := 403
		if tc.allowed {
			want = 204
		}
		if w.Code != want {
			t.Errorf("%s %s: %d want %d", tc.method, tc.path, w.Code, want)
		}
		if tc.allowed {
			r.Header.Del("Authorization")
			w = httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 401 {
				t.Errorf("auth bypass: %s", tc.path)
			}
		}
	}
	f.activate()
	r := httptest.NewRequest("POST", "/api/v1/forward-rules", nil)
	r.Header.Set("Authorization", "Bearer valid")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatal("activation did not restore writes")
	}
}
