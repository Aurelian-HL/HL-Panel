package panellicenseissuer

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	p "github.com/hongle/hl-panel/internal/panellicense"
)

func (a *authority) prepareFree(t *testing.T, i *instance, domain string) (p.Envelope, p.Challenge) {
	t.Helper()
	r := a.request(t, i, domain)
	e, err := a.call(t, i, "free-prepare", r)
	if err != nil {
		t.Fatal(err)
	}
	var c p.Challenge
	if err := p.Verify(e, map[string]ed25519.PublicKey{"test-key": a.public}, &c); err != nil {
		t.Fatal(err)
	}
	if c.Purpose != "free-registration" || c.RequestID != r.RequestID || c.Domain != domain || c.InstanceID != i.id || c.InstanceKeyThumbprint == "" || c.DNSName != "_hl-license."+domain || c.DNSValue == "" || !strings.HasPrefix(c.ReservationID, "free_") || !c.ExpiresAt.Equal(c.IssuedAt.Add(10*time.Minute)) {
		t.Fatal("invalid free registration challenge identity, purpose, or lifetime")
	}
	return e, c
}

func (a *authority) freeRequest(t *testing.T, i *instance, domain string, challenge p.Envelope) p.Request {
	t.Helper()
	r := a.request(t, i, domain)
	r.Challenge = &challenge
	return r
}

func (a *authority) freeClaims(t *testing.T, envelope p.Envelope) p.Claims {
	t.Helper()
	var claims p.Claims
	if err := p.Verify(envelope, map[string]ed25519.PublicKey{"test-key": a.public}, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Tier != p.TierFree || claims.Limits != (p.Limits{DeviceGroups: 1, Rules: 10}) || claims.Term.Kind != "perpetual" || claims.LicenseExpiresAt != nil || claims.Status != "active" || claims.ActivatedAt.IsZero() {
		t.Fatal("free registration granted incorrect tier, limits, or status")
	}
	return claims
}

func (a *authority) requireLicenseCounts(t *testing.T, licenses, domains, reservations, audits int) {
	t.Helper()
	var gotLicenses, gotDomains, gotReservations, gotAudits int
	err := a.store.pool.QueryRow(context.Background(), `SELECT
	 (SELECT count(*) FROM hl_license_v2.licenses),
	 (SELECT count(*) FROM hl_license_v2.domains),
	 (SELECT count(*) FROM hl_license_v2.reservations),
	 (SELECT count(*) FROM hl_license_v2.audit)`).Scan(&gotLicenses, &gotDomains, &gotReservations, &gotAudits)
	if err != nil || gotLicenses != licenses || gotDomains != domains || gotReservations != reservations || gotAudits != audits {
		t.Fatalf("license/domain/reservation/audit counts = %d/%d/%d/%d, want %d/%d/%d/%d: %v", gotLicenses, gotDomains, gotReservations, gotAudits, licenses, domains, reservations, audits, err)
	}
}

func requireFreeRefusal(t *testing.T, err error) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.HTTPStatus < 400 || e.HTTPStatus >= 500 {
		t.Fatalf("want a controlled registration refusal, got %v", err)
	}
}

func TestFreePrepareIsStatelessAndDeterministic(t *testing.T) {
	clock := &testClock{value: time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)}
	issuer := newInstance(t)
	config := Config{Issuer: "https://issuer.example.test", KeyID: "test-key", SigningKey: issuer.private, AdminToken: strings.Repeat("t", 32), Clock: clock.now}
	// A nil repository makes any accidental persistent-state access fail this
	// test. Anonymous prepare must not need an operational database at all.
	s, err := newService(nil, config)
	if err != nil {
		t.Fatal(err)
	}
	i := newInstance(t)
	r := p.Request{Version: p.Version, Product: p.Product, Audience: p.Audience,
		RequestID: testKey(t, "request_"), IssuedAt: clock.now(),
		Domain: "stateless-free.example.test", InstanceID: i.id, InstancePublicKey: i.public}
	signed, _ := p.SignRequest("free-prepare", r, i.private)
	first, err := s.Public(context.Background(), "free-prepare", signed)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]ed25519.PublicKey{"test-key": config.SigningKey.Public().(ed25519.PublicKey)}
	var original p.Challenge
	if err := p.Verify(first, keys, &original); err != nil {
		t.Fatal(err)
	}
	if original.Purpose != "free-registration" || original.RequestID != r.RequestID || original.Domain != r.Domain || original.InstanceID != i.id || !original.IssuedAt.Equal(r.IssuedAt) || !original.ExpiresAt.Equal(r.IssuedAt.Add(10*time.Minute)) {
		t.Fatal("stateless prepare lost signed identity or fixed request lifetime")
	}
	clock.add(time.Minute)
	again, err := s.Public(context.Background(), "free-prepare", signed)
	if err != nil || again != first {
		t.Fatal("same stateless request changed its challenge during retry", err)
	}
	restarted, err := newService(nil, config)
	if err != nil {
		t.Fatal(err)
	}
	afterRestart, err := restarted.Public(context.Background(), "free-prepare", signed)
	if err != nil || afterRestart != first {
		t.Fatal("stateless challenge changed after issuer restart", err)
	}
	other := newInstance(t)
	for _, item := range []struct {
		name   string
		mutate func(*p.Request)
		key    ed25519.PrivateKey
	}{
		{"new request", func(r *p.Request) { r.RequestID = testKey(t, "request_") }, i.private},
		{"new identity", func(r *p.Request) { r.InstanceID, r.InstancePublicKey = other.id, other.public }, other.private},
	} {
		t.Run(item.name, func(t *testing.T) {
			changed := r
			item.mutate(&changed)
			request, _ := p.SignRequest("free-prepare", changed, item.key)
			envelope, err := restarted.Public(context.Background(), "free-prepare", request)
			if err != nil {
				t.Fatal(err)
			}
			var challenge p.Challenge
			if err := p.Verify(envelope, keys, &challenge); err != nil {
				t.Fatal(err)
			}
			if envelope == first || challenge.DNSValue == original.DNSValue || challenge.ReservationID == original.ReservationID {
				t.Fatal("different signed identity/request reused a challenge nonce")
			}
		})
	}
}

func TestPostgresFreeRegistrationDNSLifecycleAndRecovery(t *testing.T) {
	a := testAuthority(t)
	i := newInstance(t)
	domain := "free-lifecycle.example.test"
	challenge, c := a.prepareFree(t, i, domain)
	a.requireLicenseCounts(t, 0, 0, 0, 0)
	r := a.freeRequest(t, i, domain, challenge)
	_, err := a.call(t, i, "free-commit", r)
	requireCode(t, err, "dns_not_verified")
	a.requireLicenseCounts(t, 0, 0, 0, 0)
	a.dns.set(c)
	envelope, err := a.call(t, i, "free-commit", r)
	if err != nil {
		t.Fatal(err)
	}
	claims := a.freeClaims(t, envelope)
	if claims.Domain != domain || claims.InstanceID != i.id || claims.RequestID != r.RequestID {
		t.Fatal("free license bound to another request or identity")
	}
	a.requireLicenseCounts(t, 1, 1, 0, 1)
	// Exact request recovery remains durable across an issuer restart, even if
	// the operator has removed the temporary TXT record after registration.
	a.dns.mu.Lock()
	delete(a.dns.records, c.DNSName)
	a.dns.mu.Unlock()
	restarted, err := newService(a.store, a.s.config)
	if err != nil {
		t.Fatal(err)
	}
	signed, _ := p.SignRequest("free-commit", r, i.private)
	again, err := restarted.Public(context.Background(), "free-commit", signed)
	if err != nil || again != envelope {
		t.Fatal("free registration durable replay changed signed result", err)
	}
	// A fresh registration for the exact binding recovers the same license,
	// preserving its revision and activation date instead of creating a new one.
	second, nextChallenge := a.prepareFree(t, i, domain)
	a.dns.set(nextChallenge)
	next, err := a.call(t, i, "free-commit", a.freeRequest(t, i, domain, second))
	if err != nil {
		t.Fatal(err)
	}
	recovered := a.freeClaims(t, next)
	if recovered.LicenseID != claims.LicenseID || recovered.Revision != claims.Revision || !recovered.ActivatedAt.Equal(claims.ActivatedAt) {
		t.Fatal("free recovery created or reset a license")
	}
	a.requireLicenseCounts(t, 1, 1, 0, 2)
	synced, err := a.sync(t, i, domain, claims.LicenseID)
	if err != nil || synced.Tier != p.TierFree || synced.Limits != claims.Limits {
		t.Fatal("free license lost entitlements during normal sync", err)
	}
}

func TestPostgresFreeRegistrationChallengeValidation(t *testing.T) {
	a := testAuthority(t)
	i := newInstance(t)
	domain := "free-challenge.example.test"
	envelope, c := a.prepareFree(t, i, domain)
	a.dns.set(c)
	cases := []struct {
		name   string
		mutate func(*p.Challenge)
	}{
		{"paid purpose", func(c *p.Challenge) { c.Purpose = "" }},
		{"unknown purpose", func(c *p.Challenge) { c.Purpose = "licensed-registration" }},
		{"version", func(c *p.Challenge) { c.Version++ }},
		{"issuer", func(c *p.Challenge) { c.Issuer = "https://other-issuer.example.test" }},
		{"product", func(c *p.Challenge) { c.Product = "another-product" }},
		{"audience", func(c *p.Challenge) { c.Audience = "another-audience" }},
		{"domain", func(c *p.Challenge) { c.Domain = "other.example.test" }},
		{"instance", func(c *p.Challenge) { c.InstanceID = "other-instance" }},
		{"public key", func(c *p.Challenge) { c.InstanceKeyThumbprint = strings.Repeat("0", 64) }},
		{"missing request identity", func(c *p.Challenge) { c.RequestID = "" }},
		{"missing reservation", func(c *p.Challenge) { c.ReservationID = "" }},
		{"paid reservation", func(c *p.Challenge) { c.ReservationID = "res_paid-reservation" }},
		{"DNS name", func(c *p.Challenge) { c.DNSName = "_hl-license.other.example.test" }},
		{"DNS value", func(c *p.Challenge) { c.DNSValue = "" }},
		{"expired", func(c *p.Challenge) { c.ExpiresAt = a.clock.now() }},
		{"future issued time", func(c *p.Challenge) { c.IssuedAt = a.clock.now().Add(time.Hour) }},
		{"excess lifetime", func(c *p.Challenge) { c.ExpiresAt = c.IssuedAt.Add(24 * time.Hour) }},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			bad := c
			item.mutate(&bad)
			signed, err := p.Sign(bad, a.s.config.KeyID, a.s.config.SigningKey)
			if err != nil {
				t.Fatal(err)
			}
			_, err = a.call(t, i, "free-commit", a.freeRequest(t, i, domain, signed))
			requireFreeRefusal(t, err)
			a.requireLicenseCounts(t, 0, 0, 0, 0)
		})
	}
	t.Run("untrusted signature", func(t *testing.T) {
		bad := envelope
		bad.Signature = strings.Repeat("A", len(bad.Signature))
		_, err := a.call(t, i, "free-commit", a.freeRequest(t, i, domain, bad))
		requireFreeRefusal(t, err)
	})
	t.Run("modified payload", func(t *testing.T) {
		bad := envelope
		changed := c
		changed.Purpose = "paid"
		bad.Payload, _ = p.Encode(changed)
		_, err := a.call(t, i, "free-commit", a.freeRequest(t, i, domain, bad))
		requireFreeRefusal(t, err)
	})
	t.Run("unknown signing key", func(t *testing.T) {
		bad := envelope
		bad.KeyID = "other-key"
		_, err := a.call(t, i, "free-commit", a.freeRequest(t, i, domain, bad))
		requireFreeRefusal(t, err)
	})
	a.requireLicenseCounts(t, 0, 0, 0, 0)
	// Time advances while a newly signed commit request remains current; the
	// expired challenge itself must be rejected, independently of request time.
	a.clock.add(10 * time.Minute)
	_, err := a.call(t, i, "free-commit", a.freeRequest(t, i, domain, envelope))
	requireFreeRefusal(t, err)
	a.requireLicenseCounts(t, 0, 0, 0, 0)
}

func TestPostgresFreeRegistrationOperationSeparation(t *testing.T) {
	a := testAuthority(t)
	i := newInstance(t)
	domain := "free-operation.example.test"
	e, c := a.prepareFree(t, i, domain)
	a.dns.set(c)
	for _, op := range []string{"free-prepare", "free-commit"} {
		for _, field := range []string{"code", "license", "reservation"} {
			t.Run(op+" rejects "+field, func(t *testing.T) {
				r := a.request(t, i, domain)
				if op == "free-commit" {
					r.Challenge = &e
				}
				switch field {
				case "code":
					r.Code = strings.Repeat("x", 30)
				case "license":
					r.LicenseID = "lic_unrelated"
				case "reservation":
					r.ReservationID = c.ReservationID
				}
				_, err := a.call(t, i, op, r)
				requireCode(t, err, "invalid_request")
			})
		}
	}
	_, err := a.call(t, i, "free-prepare", a.freeRequest(t, i, domain, e))
	requireCode(t, err, "invalid_request")
	_, err = a.call(t, i, "free-commit", a.request(t, i, domain))
	requireCode(t, err, "invalid_request")
	for _, op := range []string{"prepare", "commit", "sync"} {
		_, err = a.call(t, i, op, a.freeRequest(t, i, domain, e))
		requireCode(t, err, "invalid_request")
	}
	r := a.freeRequest(t, i, domain, e)
	wrongOperationSignature, _ := p.SignRequest("commit", r, i.private)
	_, err = a.s.Public(context.Background(), "free-commit", wrongOperationSignature)
	requireCode(t, err, "invalid_request")
	a.requireLicenseCounts(t, 0, 0, 0, 0)
}

func TestPostgresFreeRegistrationBindingConflicts(t *testing.T) {
	a := testAuthority(t)
	i := newInstance(t)
	domain := "free-binding.example.test"
	e, c := a.prepareFree(t, i, domain)
	a.dns.set(c)
	other := newInstance(t)
	_, err := a.call(t, other, "free-commit", a.freeRequest(t, other, domain, e))
	requireFreeRefusal(t, err)
	a.requireLicenseCounts(t, 0, 0, 0, 0)
	registered, err := a.call(t, i, "free-commit", a.freeRequest(t, i, domain, e))
	if err != nil {
		t.Fatal(err)
	}
	claims := a.freeClaims(t, registered)
	// Knowing DNS ownership cannot silently replace the registered instance or
	// its key, even when one half of the original binding is reused.
	for _, tc := range []struct {
		name string
		i    *instance
	}{
		{"different instance", other},
		{"same instance different key", &instance{id: i.id, private: other.private, public: other.public}},
		{"same key different instance", &instance{id: other.id, private: i.private, public: i.public}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next, challenge := a.prepareFree(t, tc.i, domain)
			a.dns.set(challenge)
			_, err := a.call(t, tc.i, "free-commit", a.freeRequest(t, tc.i, domain, next))
			requireFreeRefusal(t, err)
		})
	}
	a.requireLicenseCounts(t, 1, 1, 0, 1)
	synced, err := a.sync(t, i, domain, claims.LicenseID)
	if err != nil || synced.InstanceID != i.id {
		t.Fatal("failed takeover damaged the original binding", err)
	}
}

func TestPostgresFreeRegistrationCannotBypassLicensedState(t *testing.T) {
	for _, state := range []string{"pending", "active", "expired", "revoked"} {
		t.Run(state, func(t *testing.T) {
			a := testAuthority(t)
			i := newInstance(t)
			domain := "licensed-" + state + ".example.test"
			issued := a.issue(t, domain)
			var active p.Claims
			if state != "pending" {
				active = a.commit(t, i, a.prepare(t, i, issued))
			}
			if state == "expired" {
				a.clock.add(48 * time.Hour)
			}
			if state == "revoked" {
				_, err := a.s.Mutate(context.Background(), issued.License.ID, "revoke", testKey(t, "revoke_"), mutation(active.Revision))
				if err != nil {
					t.Fatal(err)
				}
			}
			e, c := a.prepareFree(t, i, domain)
			a.dns.set(c)
			_, err := a.call(t, i, "free-commit", a.freeRequest(t, i, domain, e))
			requireFreeRefusal(t, err)
			list, err := a.s.List(context.Background())
			if err != nil || len(list) != 1 || list[0].ID != issued.License.ID || list[0].Tier != p.TierLicensed || list[0].Status != state {
				t.Fatal("free registration replaced or changed a licensed record", err)
			}
		})
	}
}

func TestPostgresFreeRegistrationRevocationAndDomainHistory(t *testing.T) {
	a := testAuthority(t)
	i := newInstance(t)
	domain := "free-revoked.example.test"
	e, c := a.prepareFree(t, i, domain)
	a.dns.set(c)
	envelope, err := a.call(t, i, "free-commit", a.freeRequest(t, i, domain, e))
	if err != nil {
		t.Fatal(err)
	}
	claims := a.freeClaims(t, envelope)
	_, err = a.s.Mutate(context.Background(), claims.LicenseID, "revoke", testKey(t, "revoke_"), mutation(claims.Revision))
	if err != nil {
		t.Fatal(err)
	}
	fresh, freshChallenge := a.prepareFree(t, i, domain)
	a.dns.set(freshChallenge)
	_, err = a.call(t, i, "free-commit", a.freeRequest(t, i, domain, fresh))
	requireFreeRefusal(t, err)
	// An administrative rebind retains the old domain claim. It must not let a
	// free registration create a second license on that historical domain.
	issued := a.issue(t, "former-paid.example.test")
	input := mutation(issued.License.Revision)
	input.Domain = "new-paid.example.test"
	_, err = a.s.Mutate(context.Background(), issued.License.ID, "rebind", testKey(t, "rebind_"), input)
	if err != nil {
		t.Fatal(err)
	}
	old, oldChallenge := a.prepareFree(t, i, issued.License.Domain)
	a.dns.set(oldChallenge)
	_, err = a.call(t, i, "free-commit", a.freeRequest(t, i, issued.License.Domain, old))
	requireFreeRefusal(t, err)
	list, err := a.s.List(context.Background())
	if err != nil || len(list) != 2 {
		t.Fatal("revocation or historical claim bypass created another license", err)
	}
}

func TestPostgresFreeRegistrationAuditedCodeRecoveryAndRebind(t *testing.T) {
	a := testAuthority(t)
	ctx := context.Background()
	originalInstance := newInstance(t)
	originalDomain := "free-before-recovery.example.test"
	challenge, c := a.prepareFree(t, originalInstance, originalDomain)
	a.dns.set(c)
	envelope, err := a.call(t, originalInstance, "free-commit", a.freeRequest(t, originalInstance, originalDomain, challenge))
	if err != nil {
		t.Fatal(err)
	}
	original := a.freeClaims(t, envelope)
	requireRecoveredClaims := func(claims p.Claims, domain string, instance *instance, revision int64) {
		t.Helper()
		if claims.LicenseID != original.LicenseID || claims.Domain != domain || claims.InstanceID != instance.id ||
			claims.Revision != revision || claims.Tier != p.TierFree || claims.Limits != original.Limits ||
			claims.Term.Kind != "perpetual" || claims.LicenseExpiresAt != nil || claims.Status != "active" ||
			!claims.ActivatedAt.Equal(original.ActivatedAt) {
			t.Fatal("administrative recovery changed free entitlements, license identity, or activation date")
		}
	}
	// Initial registration needs no code; an explicit audited administrator
	// recovery may issue one without upgrading the existing free license.
	rotateInput := mutation(original.Revision)
	rotateInput.Reason = "recover existing free credential"
	rotated, err := a.s.Mutate(ctx, original.LicenseID, "rotate-code", testKey(t, "rotate_"), rotateInput)
	if err != nil || !strings.HasPrefix(rotated.Code, "hl2_") || rotated.License.InstanceID != originalInstance.id ||
		rotated.License.ID != original.LicenseID || rotated.License.Tier != p.TierFree || rotated.License.Limits != original.Limits {
		t.Fatal("audited free credential rotation lost its original binding or tier", err)
	}
	rotatedClaims := a.commit(t, originalInstance, a.prepare(t, originalInstance, rotated))
	requireRecoveredClaims(rotatedClaims, originalDomain, originalInstance, rotated.License.Revision)
	synced, err := a.sync(t, originalInstance, originalDomain, original.LicenseID)
	if err != nil {
		t.Fatal(err)
	}
	requireRecoveredClaims(synced, originalDomain, originalInstance, rotatedClaims.Revision)

	newDomain := "free-after-recovery.example.test"
	newInstance := newInstance(t)
	rebindInput := mutation(rotatedClaims.Revision)
	rebindInput.Domain = newDomain
	rebindInput.Reason = "move free instance after verified support request"
	rebound, err := a.s.Mutate(ctx, original.LicenseID, "rebind", testKey(t, "rebind_"), rebindInput)
	if err != nil || !strings.HasPrefix(rebound.Code, "hl2_") || rebound.Code == rotated.Code ||
		rebound.License.InstanceID != "" || rebound.License.ID != original.LicenseID ||
		rebound.License.Domain != newDomain || rebound.License.Tier != p.TierFree || rebound.License.Limits != original.Limits {
		t.Fatal("audited free rebind lost the license identity or entitlements", err)
	}
	_, err = a.sync(t, originalInstance, originalDomain, original.LicenseID)
	requireCode(t, err, "binding_mismatch")
	// A historical domain stays claimed even after the free license moves.
	oldChallenge, old := a.prepareFree(t, originalInstance, originalDomain)
	a.dns.set(old)
	_, err = a.call(t, originalInstance, "free-commit", a.freeRequest(t, originalInstance, originalDomain, oldChallenge))
	requireCode(t, err, "domain_conflict")
	// The explicit recovery credential still requires DNS ownership and a
	// matching instance signature before it establishes the new binding.
	newChallenge := a.prepare(t, newInstance, rebound)
	commitRequest := a.request(t, newInstance, newDomain)
	commitRequest.ReservationID = newChallenge.ReservationID
	_, err = a.call(t, newInstance, "commit", commitRequest)
	requireCode(t, err, "dns_not_verified")
	bound := a.commit(t, newInstance, newChallenge)
	requireRecoveredClaims(bound, newDomain, newInstance, rebound.License.Revision+1)
	synced, err = a.sync(t, newInstance, newDomain, original.LicenseID)
	if err != nil {
		t.Fatal(err)
	}
	requireRecoveredClaims(synced, newDomain, newInstance, bound.Revision)

	audit, err := a.s.Audit(ctx, original.LicenseID)
	if err != nil {
		t.Fatal(err)
	}
	managementActions := map[string]p.AuditEvent{}
	for _, event := range audit {
		if event.Action == "rotate-code" || event.Action == "rebind" {
			if _, exists := managementActions[event.Action]; exists {
				t.Fatal("administrative recovery created duplicate audit events")
			}
			managementActions[event.Action] = event
		}
	}
	for action, input := range map[string]p.MutationRequest{"rotate-code": rotateInput, "rebind": rebindInput} {
		event, ok := managementActions[action]
		if !ok || event.LicenseID != original.LicenseID || event.Actor != input.Actor || event.Reason != input.Reason ||
			event.Revision != input.ExpectedRevision+1 {
			t.Fatal("free administrator recovery did not preserve its actor, reason, and revision audit")
		}
	}
	a.requireLicenseCounts(t, 1, 2, 1, 6)
}

func TestPostgresFreeRegistrationConcurrentCommit(t *testing.T) {
	for _, sameBinding := range []bool{true, false} {
		name := "different bindings"
		if sameBinding {
			name = "same binding"
		}
		t.Run(name, func(t *testing.T) {
			a := testAuthority(t)
			one, two := newInstance(t), newInstance(t)
			if sameBinding {
				two = one
			}
			domain := "free-race.example.test"
			e1, c1 := a.prepareFree(t, one, domain)
			e2, c2 := a.prepareFree(t, two, domain)
			a.dns.mu.Lock()
			a.dns.records[c1.DNSName] = []string{c1.DNSValue, c2.DNSValue}
			a.dns.mu.Unlock()
			type result struct {
				envelope p.Envelope
				err      error
			}
			results := make(chan result, 2)
			var wg sync.WaitGroup
			for _, item := range []struct {
				i *instance
				e p.Envelope
			}{{one, e1}, {two, e2}} {
				r := a.freeRequest(t, item.i, domain, item.e)
				signed, _ := p.SignRequest("free-commit", r, item.i.private)
				wg.Go(func() {
					e, err := a.s.Public(context.Background(), "free-commit", signed)
					results <- result{e, err}
				})
			}
			wg.Wait()
			close(results)
			count, id := 0, ""
			for result := range results {
				if result.err != nil {
					requireFreeRefusal(t, result.err)
					continue
				}
				claims := a.freeClaims(t, result.envelope)
				if id != "" && claims.LicenseID != id {
					t.Fatal("concurrent commits issued different licenses")
				}
				id = claims.LicenseID
				count++
			}
			want := 1
			if sameBinding {
				want = 2
			}
			if count != want {
				t.Fatalf("successful registrations %d, want %d", count, want)
			}
			a.requireLicenseCounts(t, 1, 1, 0, want)
		})
	}
}

func TestPostgresFreeRegistrationRacesAdministrativeClaim(t *testing.T) {
	a := testAuthority(t)
	i := newInstance(t)
	domain := "free-admin-race.example.test"
	e, c := a.prepareFree(t, i, domain)
	a.dns.set(c)
	r := a.freeRequest(t, i, domain, e)
	signed, _ := p.SignRequest("free-commit", r, i.private)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Go(func() {
		_, err := a.s.Public(context.Background(), "free-commit", signed)
		errs <- err
	})
	key := testKey(t, "admin-race_")
	wg.Go(func() {
		_, err := a.s.Issue(context.Background(), key, issueInput(domain))
		errs <- err
	})
	wg.Wait()
	close(errs)
	success := 0
	for err := range errs {
		if err == nil {
			success++
		} else {
			requireFreeRefusal(t, err)
		}
	}
	if success != 1 {
		t.Fatalf("free/administrative race successful registrations %d, want 1", success)
	}
	a.requireLicenseCounts(t, 1, 1, 0, 1)
}

type freeDomainSignalRepository struct {
	repository
	entered chan struct{}
}

func (s *freeDomainSignalRepository) Run(ctx context.Context, key, digest string, fn func(transaction) ([]byte, error)) ([]byte, bool, error) {
	return s.repository.Run(ctx, key, digest, func(tx transaction) ([]byte, error) {
		return fn(&freeDomainSignalTransaction{transaction: tx, entered: s.entered})
	})
}

type freeDomainSignalTransaction struct {
	transaction
	entered chan struct{}
}

func (tx *freeDomainSignalTransaction) ByDomain(ctx context.Context, domain string) (license, error) {
	select {
	case tx.entered <- struct{}{}:
	default:
	}
	return tx.transaction.ByDomain(ctx, domain)
}

func TestPostgresFreeRegistrationRechecksExpiryAfterDomainLock(t *testing.T) {
	a := testAuthority(t)
	i := newInstance(t)
	domain := "free-lock-expiry.example.test"
	e, c := a.prepareFree(t, i, domain)
	a.dns.set(c)
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
	r := a.freeRequest(t, i, domain, e)
	signed, _ := p.SignRequest("free-commit", r, i.private)
	finished := make(chan error, 1)
	go func() {
		_, err := s.Public(ctx, "free-commit", signed)
		finished <- err
	}()
	select {
	case <-observed.entered:
	case <-ctx.Done():
		t.Fatal("registration never reached the held domain lock")
	}
	a.clock.add(10 * time.Minute)
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		requireCode(t, err, "challenge_expired")
	case <-ctx.Done():
		t.Fatal("registration did not finish after domain lock release")
	}
	a.requireLicenseCounts(t, 0, 0, 0, 0)
}

func TestPostgresFreeTierChangesPreserveIdentityAndUseCurrentTime(t *testing.T) {
	a := testAuthority(t)
	i := newInstance(t)
	domain := "tier-change.example.test"
	e, c := a.prepareFree(t, i, domain)
	a.dns.set(c)
	envelope, err := a.call(t, i, "free-commit", a.freeRequest(t, i, domain, e))
	if err != nil {
		t.Fatal(err)
	}
	free := a.freeClaims(t, envelope)
	a.clock.add(30 * 24 * time.Hour)
	input := mutation(free.Revision)
	input.Tier = p.TierLicensed
	input.Term = &p.Term{Kind: "days", Days: 7}
	upgraded, err := a.s.Mutate(context.Background(), free.LicenseID, "change-tier", testKey(t, "upgrade_"), input)
	if err != nil {
		t.Fatal(err)
	}
	if upgraded.License.Tier != p.TierLicensed || upgraded.License.Limits != (p.Limits{}) || upgraded.License.ID != free.LicenseID || upgraded.License.InstanceID != i.id || !upgraded.License.ActivatedAt.Equal(free.ActivatedAt) || upgraded.License.LicenseExpiresAt == nil || !upgraded.License.LicenseExpiresAt.Equal(a.clock.now().Add(7*24*time.Hour)) {
		t.Fatal("upgrade changed identity or started the paid term before upgrade time")
	}
	// Same-tier requests may not use change-tier to shorten a formal license.
	input.ExpectedRevision = upgraded.License.Revision
	_, err = a.s.Mutate(context.Background(), free.LicenseID, "change-tier", testKey(t, "same-tier_"), input)
	requireFreeRefusal(t, err)
	input.Tier = p.TierFree
	input.Term = &p.Term{Kind: "perpetual"}
	downgraded, err := a.s.Mutate(context.Background(), free.LicenseID, "change-tier", testKey(t, "downgrade_"), input)
	if err != nil {
		t.Fatal(err)
	}
	if downgraded.License.Tier != p.TierFree || downgraded.License.Limits != free.Limits || downgraded.License.ID != free.LicenseID || downgraded.License.InstanceID != i.id || downgraded.License.LicenseExpiresAt != nil || !downgraded.License.ActivatedAt.Equal(free.ActivatedAt) {
		t.Fatal("authorized downgrade failed to preserve the original binding")
	}
	// A pending free license must start its new paid term at activation.
	pending := a.issue(t, "pending-free-upgrade.example.test")
	toFree := mutation(pending.License.Revision)
	toFree.Tier = p.TierFree
	toFree.Term = &p.Term{Kind: "perpetual"}
	pendingFree, err := a.s.Mutate(context.Background(), pending.License.ID, "change-tier", testKey(t, "pending-free_"), toFree)
	if err != nil {
		t.Fatal(err)
	}
	change := mutation(pendingFree.License.Revision)
	change.Tier = p.TierLicensed
	change.Term = &p.Term{Kind: "days", Days: 3}
	paid, err := a.s.Mutate(context.Background(), pending.License.ID, "change-tier", testKey(t, "pending-upgrade_"), change)
	if err != nil || paid.License.ActivatedAt != nil || paid.License.LicenseExpiresAt != nil || paid.License.Status != "pending" {
		t.Fatal("pending upgrade started the paid term before activation", err)
	}
	paid.Code = pending.Code
	a.clock.add(48 * time.Hour)
	activated := a.commit(t, i, a.prepare(t, i, paid))
	if activated.LicenseExpiresAt == nil || !activated.LicenseExpiresAt.Equal(a.clock.now().Add(3*24*time.Hour)) {
		t.Fatal("pending upgraded term did not start at activation")
	}
}
