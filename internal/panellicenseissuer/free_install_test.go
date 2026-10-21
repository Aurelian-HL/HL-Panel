package panellicenseissuer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	p "github.com/hongle/hl-panel/internal/panellicense"
)

func TestFreeInstallPrepareStatelessSignedAndOperationBound(t *testing.T) {
	clock := &testClock{value: time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)}
	issuer, i := newInstance(t), newInstance(t)
	config := Config{Issuer: "https://issuer.example.test", KeyID: "test-key", SigningKey: issuer.private, AdminToken: strings.Repeat("t", 32), Clock: clock.now}
	s, err := newService(nil, config)
	if err != nil {
		t.Fatal(err)
	}
	r := p.Request{Version: p.Version, Product: p.Product, Audience: p.Audience, RequestID: testKey(t, "request_"),
		IssuedAt: clock.now(), Domain: "new-install.example.test", InstanceID: i.id, InstancePublicKey: i.public}
	signed, _ := p.SignRequest("free-install-prepare", r, i.private)
	first, err := s.Public(context.Background(), "free-install-prepare", signed)
	if err != nil {
		t.Fatal(err)
	}
	var c p.Challenge
	if err := p.Verify(first, map[string]ed25519.PublicKey{"test-key": issuer.private.Public().(ed25519.PublicKey)}, &c); err != nil {
		t.Fatal(err)
	}
	if c.Purpose != freeInstallPurpose || c.VerificationMethod != "http-01" || c.HTTPPath != freeInstallHTTPPrefix+c.ReservationID ||
		!strings.HasPrefix(c.ReservationID, "free_") || !validFreeNonce(c.ReservationID[5:]) ||
		!strings.HasPrefix(c.HTTPValue, "hl-license-v2=") || !validFreeNonce(c.HTTPValue[14:]) ||
		c.DNSName != "" || c.DNSValue != "" || c.Domain != r.Domain || c.RequestID != r.RequestID || c.InstanceID != i.id ||
		c.InstanceKeyThumbprint == "" || !c.IssuedAt.Equal(r.IssuedAt) || !c.ExpiresAt.Equal(r.IssuedAt.Add(10*time.Minute)) {
		t.Fatal("automatic install challenge lost protocol, identity, nonce or lifetime binding")
	}
	clock.add(time.Minute)
	restarted, _ := newService(nil, config)
	again, err := restarted.Public(context.Background(), "free-install-prepare", signed)
	if err != nil || again != first {
		t.Fatal("stateless installation challenge changed on retry/restart", err)
	}
	if _, err := restarted.Public(context.Background(), "free-prepare", signed); err == nil {
		t.Fatal("installation signature accepted for manual DNS operation")
	}
	r.Challenge = &first
	if _, err := restarted.verifyFreeChallenge(r); err == nil {
		t.Fatal("HTTP challenge accepted by DNS registration")
	}
	r.Challenge = nil
	dnsSigned, _ := p.SignRequest("free-prepare", r, i.private)
	dns, _ := restarted.Public(context.Background(), "free-prepare", dnsSigned)
	r.Challenge = &dns
	if _, err := restarted.verifyFreeInstallChallenge(r); err == nil {
		t.Fatal("DNS challenge accepted by HTTP registration")
	}
	for _, input := range []p.Request{
		func() p.Request { v := r; v.Code = "not-a-license-code"; v.Challenge = nil; return v }(),
		func() p.Request { v := r; v.LicenseID = "lic_existing"; v.Challenge = nil; return v }(),
		func() p.Request { v := r; v.ReservationID = "res_existing"; v.Challenge = nil; return v }(),
		r,
	} {
		proof, _ := p.SignRequest("free-install-prepare", input, i.private)
		if _, err := restarted.Public(context.Background(), "free-install-prepare", proof); err == nil {
			t.Fatal("invalid automatic prepare fields accepted")
		}
	}
	// Exercise the actual public JSON route, not just the service dispatch.
	server := httptest.NewServer(restarted.Handler())
	defer server.Close()
	r.Challenge = nil
	for _, operation := range []string{"prepare", "commit"} {
		request, _ := p.SignRequest("free-install-"+operation, r, i.private)
		raw, _ := json.Marshal(request)
		response, err := http.Post(server.URL+p.PublicPath+"/free/install/"+operation, "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		want := 200
		if operation == "commit" {
			want = 400
		}
		if response.StatusCode != want {
			t.Fatalf("automatic %s HTTP status %d, want %d", operation, response.StatusCode, want)
		}
	}
}

type installationProof struct {
	mu     sync.RWMutex
	values map[string]string
	calls  atomic.Int32
}

func (v *installationProof) set(c p.Challenge) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.values[c.Domain+c.HTTPPath] = c.HTTPValue
}

func testInstallAuthority(t *testing.T) (*authority, *installationProof) {
	t.Helper()
	a := testAuthority(t)
	proof := &installationProof{values: map[string]string{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proof.calls.Add(1)
		proof.mu.RLock()
		value, exists := proof.values[r.Host+r.URL.Path]
		proof.mu.RUnlock()
		if !exists || r.Method != http.MethodGet {
			w.WriteHeader(404)
			return
		}
		fmt.Fprint(w, value)
	}))
	t.Cleanup(server.Close)
	// Production public-IP checks still run. Only the socket dial maps this
	// synthetic public answer into a real loopback HTTP proof server.
	a.s.config.HTTPVerifier = &httpProofVerifier{
		resolver: proofResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		}),
		dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != "8.8.8.8:80" {
				return nil, ErrHTTP
			}
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		},
	}
	return a, proof
}

func (a *authority) prepareInstall(t *testing.T, i *instance, domain string) (p.Envelope, p.Challenge) {
	t.Helper()
	e, err := a.call(t, i, "free-install-prepare", a.request(t, i, domain))
	if err != nil {
		t.Fatal(err)
	}
	var c p.Challenge
	if err := p.Verify(e, map[string]ed25519.PublicKey{"test-key": a.public}, &c); err != nil {
		t.Fatal(err)
	}
	return e, c
}

func TestPostgresFreeInstallActualHTTPRegistrationReplayAndRecovery(t *testing.T) {
	a, proof := testInstallAuthority(t)
	i, domain := newInstance(t), "automatic-free.example.test"
	e, c := a.prepareInstall(t, i, domain)
	a.requireLicenseCounts(t, 0, 0, 0, 0)
	r := a.freeRequest(t, i, domain, e)
	if _, err := a.call(t, i, "free-install-commit", r); err == nil {
		t.Fatal("registration accepted missing HTTP proof")
	} else {
		requireCode(t, err, "http_not_verified")
	}
	a.requireLicenseCounts(t, 0, 0, 0, 0)
	proof.set(c)
	result, err := a.call(t, i, "free-install-commit", r)
	if err != nil {
		t.Fatal(err)
	}
	claims := a.freeClaims(t, result)
	if claims.Domain != domain || claims.InstanceID != i.id || claims.RequestID != r.RequestID {
		t.Fatal("automatic free license identity mismatch")
	}
	a.requireLicenseCounts(t, 1, 1, 0, 1)
	// Registration is visible through the same management endpoint HT reads.
	items, err := a.s.List(context.Background())
	if err != nil || len(items) != 1 || items[0].ID != claims.LicenseID || items[0].Tier != p.TierFree || items[0].Limits != claims.Limits {
		t.Fatal("automatic install missing from management registration", err)
	}
	proof.mu.Lock()
	clear(proof.values)
	proof.mu.Unlock()
	httpCalls := proof.calls.Load()
	restarted, err := newService(a.store, a.s.config)
	if err != nil {
		t.Fatal(err)
	}
	signed, _ := p.SignRequest("free-install-commit", r, i.private)
	again, err := restarted.Public(context.Background(), "free-install-commit", signed)
	if err != nil || again != result || proof.calls.Load() != httpCalls {
		t.Fatal("durable replay performed HTTP or changed signed claims", err)
	}
	conflicting := r
	conflicting.Domain = "other-install.example.test"
	conflictingSigned, _ := p.SignRequest("free-install-commit", conflicting, i.private)
	_, err = restarted.Public(context.Background(), "free-install-commit", conflictingSigned)
	requireCode(t, err, "idempotency_conflict")
	if proof.calls.Load() != httpCalls {
		t.Fatal("idempotency conflict made an outbound HTTP request")
	}
	nextEnvelope, nextChallenge := a.prepareInstall(t, i, domain)
	proof.set(nextChallenge)
	recoveredEnvelope, err := a.call(t, i, "free-install-commit", a.freeRequest(t, i, domain, nextEnvelope))
	if err != nil {
		t.Fatal(err)
	}
	recovered := a.freeClaims(t, recoveredEnvelope)
	if recovered.LicenseID != claims.LicenseID || recovered.Revision != claims.Revision || !recovered.ActivatedAt.Equal(claims.ActivatedAt) {
		t.Fatal("recovery reset identity, revision or activation")
	}
	a.requireLicenseCounts(t, 1, 1, 0, 2)
	audits, err := a.s.Audit(context.Background(), claims.LicenseID)
	if err != nil || len(audits) != 2 {
		t.Fatal("missing installation audits", err)
	}
	actions := map[string]bool{}
	for _, audit := range audits {
		actions[audit.Action] = true
	}
	if !actions["free-install-register"] || !actions["free-install-recover"] {
		t.Fatal("wrong install audit actions")
	}
	other := newInstance(t)
	otherEnvelope, otherChallenge := a.prepareInstall(t, other, domain)
	proof.set(otherChallenge)
	_, err = a.call(t, other, "free-install-commit", a.freeRequest(t, other, domain, otherEnvelope))
	requireCode(t, err, "domain_conflict")
	a.requireLicenseCounts(t, 1, 1, 0, 2)
	if synced, err := a.sync(t, i, domain, claims.LicenseID); err != nil || synced.Tier != p.TierFree || synced.Limits != claims.Limits {
		t.Fatal("automatic license does not use normal sync", err)
	}
}

func TestPostgresFreeInstallChallengeBindingAndExpiry(t *testing.T) {
	a, proof := testInstallAuthority(t)
	i, domain := newInstance(t), "install-binding.example.test"
	e, c := a.prepareInstall(t, i, domain)
	proof.set(c)
	for _, item := range []struct {
		name   string
		mutate func(*p.Challenge)
	}{
		{"purpose", func(c *p.Challenge) { c.Purpose = freePurpose }},
		{"issuer", func(c *p.Challenge) { c.Issuer = "https://elsewhere.example.test" }},
		{"version", func(c *p.Challenge) { c.Version++ }},
		{"product", func(c *p.Challenge) { c.Product = "elsewhere" }},
		{"audience", func(c *p.Challenge) { c.Audience = "elsewhere" }},
		{"domain", func(c *p.Challenge) { c.Domain = "elsewhere.example.test" }},
		{"instance", func(c *p.Challenge) { c.InstanceID = "other-instance" }},
		{"key", func(c *p.Challenge) { c.InstanceKeyThumbprint = strings.Repeat("0", 64) }},
		{"request", func(c *p.Challenge) { c.RequestID = "" }},
		{"reservation", func(c *p.Challenge) { c.ReservationID = "free_" + strings.Repeat("=", 32) }},
		{"method", func(c *p.Challenge) { c.VerificationMethod = "dns-01" }},
		{"path", func(c *p.Challenge) { c.HTTPPath = "/admin" }},
		{"value", func(c *p.Challenge) { c.HTTPValue = "hl-license-v2=" + strings.Repeat("=", 32) }},
		{"DNS name", func(c *p.Challenge) { c.DNSName = "_hl-license." + domain }},
		{"DNS value", func(c *p.Challenge) { c.DNSValue = c.HTTPValue }},
		{"expired", func(c *p.Challenge) { c.ExpiresAt = a.clock.now() }},
		{"future issue", func(c *p.Challenge) { c.IssuedAt = a.clock.now().Add(time.Hour) }},
		{"long lifetime", func(c *p.Challenge) { c.ExpiresAt = c.IssuedAt.Add(11 * time.Minute) }},
	} {
		t.Run(item.name, func(t *testing.T) {
			changed := c
			item.mutate(&changed)
			signed, _ := p.Sign(changed, "test-key", a.s.config.SigningKey)
			_, err := a.call(t, i, "free-install-commit", a.freeRequest(t, i, domain, signed))
			requireFreeRefusal(t, err)
		})
	}
	tampered := e
	tampered.Signature = strings.Repeat("A", len(e.Signature))
	_, err := a.call(t, i, "free-install-commit", a.freeRequest(t, i, domain, tampered))
	requireFreeRefusal(t, err)
	a.clock.add(10 * time.Minute)
	_, err = a.call(t, i, "free-install-commit", a.freeRequest(t, i, domain, e))
	requireCode(t, err, "challenge_expired")
	if proof.calls.Load() != 0 {
		t.Fatal("invalid challenge reached outbound HTTP verifier")
	}
	a.requireLicenseCounts(t, 0, 0, 0, 0)
}

func TestPostgresFreeInstallDoesNotDowngradeFormalOrRevokedLicense(t *testing.T) {
	for _, state := range []string{"pending", "active", "expired", "revoked"} {
		t.Run(state, func(t *testing.T) {
			a, proof := testInstallAuthority(t)
			i, domain := newInstance(t), "formal-"+state+".example.test"
			issued := a.issue(t, domain)
			if state != "pending" {
				a.commit(t, i, a.prepare(t, i, issued))
			}
			if state == "expired" {
				a.clock.add(48 * time.Hour)
			}
			if state == "revoked" {
				items, _ := a.s.List(context.Background())
				if _, err := a.s.Mutate(context.Background(), issued.License.ID, "revoke", testKey(t, "revoke_"), mutation(items[0].Revision)); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := a.s.List(context.Background())
			envelope, challenge := a.prepareInstall(t, i, domain)
			proof.set(challenge)
			_, err := a.call(t, i, "free-install-commit", a.freeRequest(t, i, domain, envelope))
			if state == "revoked" {
				requireCode(t, err, "license_revoked")
			} else {
				requireCode(t, err, "domain_conflict")
			}
			after, _ := a.s.List(context.Background())
			left, _ := json.Marshal(before)
			right, _ := json.Marshal(after)
			if !bytes.Equal(left, right) || len(after) != 1 || after[0].Tier != p.TierLicensed {
				t.Fatal("automatic free altered formal license")
			}
		})
	}
}

func TestPostgresFreeInstallConcurrentDomainOwnership(t *testing.T) {
	a, proof := testInstallAuthority(t)
	domain := "concurrent-install.example.test"
	type attempt struct {
		identity *instance
		request  p.Request
	}
	var attempts []attempt
	for range 6 {
		i := newInstance(t)
		e, c := a.prepareInstall(t, i, domain)
		proof.set(c)
		attempts = append(attempts, attempt{i, a.freeRequest(t, i, domain, e)})
	}
	start := make(chan struct{})
	outcomes := make(chan error, len(attempts))
	for _, item := range attempts {
		signed, _ := p.SignRequest("free-install-commit", item.request, item.identity.private)
		go func(signed p.InstanceRequest) {
			<-start
			_, err := a.s.Public(context.Background(), "free-install-commit", signed)
			outcomes <- err
		}(signed)
	}
	close(start)
	winners := 0
	for range attempts {
		if err := <-outcomes; err == nil {
			winners++
		} else {
			requireCode(t, err, "domain_conflict")
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent installation created %d winners", winners)
	}
	a.requireLicenseCounts(t, 1, 1, 0, 1)
}

func TestPostgresFreeInstallRechecksExpiryAfterDomainLock(t *testing.T) {
	a, proof := testInstallAuthority(t)
	i, domain := newInstance(t), "install-lock-expiry.example.test"
	e, c := a.prepareInstall(t, i, domain)
	proof.set(c)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	holder, err := a.store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(context.Background())
	if _, err := holder.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", digestLock("domain:"+domain)); err != nil {
		t.Fatal(err)
	}
	observed := &freeDomainSignalRepository{repository: a.store, entered: make(chan struct{}, 1)}
	s, err := newService(observed, a.s.config)
	if err != nil {
		t.Fatal(err)
	}
	signed, _ := p.SignRequest("free-install-commit", a.freeRequest(t, i, domain, e), i.private)
	finished := make(chan error, 1)
	go func() { _, err := s.Public(ctx, "free-install-commit", signed); finished <- err }()
	select {
	case <-observed.entered:
	case <-ctx.Done():
		t.Fatal("installation never reached held domain lock")
	}
	a.clock.add(10 * time.Minute)
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		requireCode(t, err, "challenge_expired")
	case <-ctx.Done():
		t.Fatal("installation did not finish after lock release")
	}
	a.requireLicenseCounts(t, 0, 0, 0, 0)
}
