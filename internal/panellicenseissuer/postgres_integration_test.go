package panellicenseissuer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	p "github.com/hongle/hl-panel/internal/panellicense"
	"github.com/jackc/pgx/v5"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeDNS struct {
	mu      sync.Mutex
	records map[string][]string
	calls   int
}

func (d *fakeDNS) LookupTXT(ctx context.Context, name string) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	return append([]string{}, d.records[name]...), nil
}
func (d *fakeDNS) set(c p.Challenge) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.records[c.DNSName] = []string{c.DNSValue}
}

type testClock struct {
	mu    sync.Mutex
	value time.Time
}

func (c *testClock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.value }
func (c *testClock) add(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.value = c.value.Add(d) }

type authority struct {
	s      *Service
	store  *Postgres
	clock  *testClock
	dns    *fakeDNS
	public ed25519.PublicKey
	token  string
}

func testAuthority(t *testing.T) *authority {
	t.Helper()
	dsn := os.Getenv("HL_LICENSE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set HL_LICENSE_TEST_DATABASE_URL for fresh loopback PostgreSQL integration")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil || config.Database != "postgres" || (config.Host != "127.0.0.1" && config.Host != "::1") {
		t.Fatal("tests only create a fresh random DB via loopback postgres maintenance DB")
	}
	ctx := context.Background()
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	id, err := randomID("")
	if err != nil {
		t.Fatal(err)
	}
	name := "hl_license_test_" + strings.ToLower(strings.NewReplacer("-", "a", "_", "b").Replace(id))
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	config.Database = name
	// ConnString returns the original parsed DSN, not later field edits.
	// Build a new loopback URL so every authority really uses its own fresh DB.
	databaseURL := url.URL{Scheme: "postgres", User: url.UserPassword(config.User, config.Password),
		Host: net.JoinHostPort(config.Host, strconv.Itoa(int(config.Port))), Path: "/" + name, RawQuery: "sslmode=disable"}
	store, err := OpenPostgres(ctx, databaseURL.String())
	if err != nil {
		admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		store.Close()
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
		admin.Close(context.Background())
	})
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := &authority{store: store, clock: &testClock{value: time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)}, dns: &fakeDNS{records: map[string][]string{}}, public: pub, token: strings.Repeat("test-management-token-", 3)}
	a.s, err = newService(store, Config{Issuer: "https://issuer.example.test", KeyID: "test-key", SigningKey: priv, AdminToken: a.token, Clock: a.clock.now, Resolver: a.dns})
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func testKey(t *testing.T, prefix string) string {
	t.Helper()
	key, err := randomID(prefix)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
func issueInput(domain string) p.IssueRequest {
	return p.IssueRequest{Domain: domain, Term: p.Term{Kind: "days", Days: 1}, Actor: p.Actor{ID: "admin-test", Name: "Test Administrator"}, Reason: "synthetic test"}
}
func (a *authority) issue(t *testing.T, domain string) p.MutationResult {
	t.Helper()
	r, err := a.s.Issue(context.Background(), testKey(t, "issue_"), issueInput(domain))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

type instance struct {
	id      string
	private ed25519.PrivateKey
	public  string
}

func newInstance(t *testing.T) *instance {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &instance{id: testKey(t, "instance_"), private: priv, public: base64.RawURLEncoding.EncodeToString(pub)}
}
func (a *authority) request(t *testing.T, i *instance, domain string) p.Request {
	t.Helper()
	return p.Request{Version: p.Version, Product: p.Product, Audience: p.Audience, RequestID: testKey(t, "request_"), IssuedAt: a.clock.now(), Domain: domain, InstanceID: i.id, InstancePublicKey: i.public}
}
func (a *authority) call(t *testing.T, i *instance, op string, r p.Request) (p.Envelope, error) {
	t.Helper()
	signed, err := p.SignRequest(op, r, i.private)
	if err != nil {
		t.Fatal(err)
	}
	return a.s.Public(context.Background(), op, signed)
}
func (a *authority) prepare(t *testing.T, i *instance, issued p.MutationResult) p.Challenge {
	t.Helper()
	r := a.request(t, i, issued.License.Domain)
	r.Code = issued.Code
	e, err := a.call(t, i, "prepare", r)
	if err != nil {
		t.Fatal(err)
	}
	var c p.Challenge
	if err = p.Verify(e, map[string]ed25519.PublicKey{"test-key": a.public}, &c); err != nil {
		t.Fatal(err)
	}
	if c.RequestID != r.RequestID || c.Domain != r.Domain || c.DNSName != "_hl-license."+r.Domain {
		t.Fatal("challenge identity mismatch")
	}
	return c
}
func (a *authority) commit(t *testing.T, i *instance, c p.Challenge) p.Claims {
	t.Helper()
	a.dns.set(c)
	r := a.request(t, i, c.Domain)
	r.ReservationID = c.ReservationID
	e, err := a.call(t, i, "commit", r)
	if err != nil {
		t.Fatal(err)
	}
	var claims p.Claims
	if err = p.Verify(e, map[string]ed25519.PublicKey{"test-key": a.public}, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}
func (a *authority) sync(t *testing.T, i *instance, domain, id string) (p.Claims, error) {
	t.Helper()
	r := a.request(t, i, domain)
	r.LicenseID = id
	e, err := a.call(t, i, "sync", r)
	if err != nil {
		return p.Claims{}, err
	}
	var c p.Claims
	err = p.Verify(e, map[string]ed25519.PublicKey{"test-key": a.public}, &c)
	return c, err
}
func mutation(revision int64) p.MutationRequest {
	return p.MutationRequest{ExpectedRevision: revision, Actor: p.Actor{ID: "admin-test", Name: "Test Administrator"}, Reason: "synthetic management change"}
}
func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func TestPostgresLifecycleAndPersistentReplay(t *testing.T) {
	a := testAuthority(t)
	ctx := context.Background()
	input := issueInput("license.example.test")
	key := testKey(t, "issue_")
	issued, err := a.s.Issue(ctx, key, input)
	if err != nil {
		t.Fatal(err)
	}
	if issued.Code == "" || issued.License.Status != "pending" {
		t.Fatal("missing initial credential")
	}
	repeated, err := a.s.Issue(ctx, key, input)
	if err != nil || !repeated.Replayed || repeated.Code != "" || repeated.License.ID != issued.License.ID {
		t.Fatal("unsafe admin replay", err)
	}
	different := input
	different.Actor.ID = "another-admin"
	_, err = a.s.Issue(ctx, key, different)
	requireCode(t, err, "idempotency_conflict")
	_, err = a.s.Issue(ctx, testKey(t, "duplicate_"), issueInput("LICENSE.EXAMPLE.TEST."))
	requireCode(t, err, "domain_conflict")
	i := newInstance(t)
	challenge := a.prepare(t, i, issued)
	same := a.prepare(t, i, issued)
	if same.ReservationID != challenge.ReservationID {
		t.Fatal("prepare not idempotent for same binding")
	}
	commit := a.request(t, i, input.Domain)
	commit.ReservationID = challenge.ReservationID
	_, err = a.call(t, i, "commit", commit)
	requireCode(t, err, "dns_not_verified")
	a.dns.set(challenge)
	envelope, err := a.call(t, i, "commit", commit)
	if err != nil {
		t.Fatal(err)
	}
	var active p.Claims
	if err = p.Verify(envelope, map[string]ed25519.PublicKey{"test-key": a.public}, &active); err != nil || active.Status != "active" || active.Revision != 2 {
		t.Fatal("not activated", err)
	}
	// A newly constructed authority over the same durable store replays the exact
	// signed response even if the DNS record is no longer available.
	restarted, err := newService(a.store, a.s.config)
	if err != nil {
		t.Fatal(err)
	}
	a.dns.mu.Lock()
	delete(a.dns.records, challenge.DNSName)
	a.dns.mu.Unlock()
	signed, _ := p.SignRequest("commit", commit, i.private)
	again, err := restarted.Public(ctx, "commit", signed)
	if err != nil || again != envelope {
		t.Fatal("durable public replay failed", err)
	}
	changed := commit
	changed.ReservationID = "different-reservation"
	_, err = a.call(t, i, "commit", changed)
	requireCode(t, err, "idempotency_conflict")
	a.clock.add(48 * time.Hour)
	expired, err := a.sync(t, i, input.Domain, active.LicenseID)
	if err != nil || expired.Status != "expired" || expired.Revision != 2 {
		t.Fatal("natural expiry changed revision", err)
	}
	request := mutation(2)
	request.Term = &p.Term{Kind: "days", Days: 3}
	extended, err := a.s.Mutate(ctx, active.LicenseID, "extend", testKey(t, "extend_"), request)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := a.sync(t, i, input.Domain, active.LicenseID)
	if err != nil || recovered.Status != "active" || recovered.Revision != 3 || recovered.LicenseID != active.LicenseID {
		t.Fatal("expired license did not recover", err)
	}
	revoked, err := a.s.Mutate(ctx, active.LicenseID, "revoke", testKey(t, "revoke_"), mutation(extended.License.Revision))
	if err != nil {
		t.Fatal(err)
	}
	revokedClaims, err := a.sync(t, i, input.Domain, active.LicenseID)
	if err != nil || revokedClaims.Status != "revoked" || revokedClaims.Revision != revoked.License.Revision {
		t.Fatal("revocation not authoritative", err)
	}
	request = mutation(revoked.License.Revision)
	request.Term = &p.Term{Kind: "days", Days: 1}
	_, err = a.s.Mutate(ctx, active.LicenseID, "extend", testKey(t, "extend_"), request)
	requireCode(t, err, "license_revoked")
	_, err = a.s.Mutate(ctx, active.LicenseID, "rebind", testKey(t, "rebind_"), mutation(revoked.License.Revision))
	requireCode(t, err, "license_revoked")
	audit, err := a.s.Audit(ctx, active.LicenseID)
	if err != nil || len(audit) != 5 {
		t.Fatalf("unexpected audit count %d %v", len(audit), err)
	}
	for _, table := range []string{"licenses", "reservations", "audit"} {
		var found bool
		err = a.store.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM hl_license_v2."+table+" WHERE document::text LIKE $1)", "%"+issued.Code+"%").Scan(&found)
		if err != nil || found {
			t.Fatal("credential persisted", table, err)
		}
	}
	var persisted bool
	err = a.store.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM hl_license_v2.operations WHERE position($1::bytea in result)>0)", []byte(issued.Code)).Scan(&persisted)
	if err != nil || persisted {
		t.Fatal("credential persisted in replay table", err)
	}
}

func TestPostgresRotateRebindAndDomainHistory(t *testing.T) {
	a := testAuthority(t)
	ctx := context.Background()
	issued := a.issue(t, "original.example.test")
	i := newInstance(t)
	c := a.prepare(t, i, issued)
	rotated, err := a.s.Mutate(ctx, issued.License.ID, "rotate-code", testKey(t, "rotate_"), mutation(1))
	if err != nil || rotated.Code == "" {
		t.Fatal(err)
	}
	oldPrepare := a.request(t, i, issued.License.Domain)
	oldPrepare.Code = issued.Code
	_, err = a.call(t, i, "prepare", oldPrepare)
	requireCode(t, err, "invalid_code")
	oldCommit := a.request(t, i, issued.License.Domain)
	oldCommit.ReservationID = c.ReservationID
	_, err = a.call(t, i, "commit", oldCommit)
	requireCode(t, err, "challenge_expired")
	bound := a.commit(t, i, a.prepare(t, i, rotated))
	rotatedAgain, err := a.s.Mutate(ctx, issued.License.ID, "rotate-code", testKey(t, "rotate_"), mutation(bound.Revision))
	if err != nil {
		t.Fatal(err)
	}
	if rotatedAgain.License.InstanceID != i.id || !rotatedAgain.License.LicenseExpiresAt.Equal(*bound.LicenseExpiresAt) {
		t.Fatal("rotation changed binding or commercial dates")
	}
	rebind := mutation(rotatedAgain.License.Revision)
	rebind.Domain = "replacement.example.test"
	rebound, err := a.s.Mutate(ctx, issued.License.ID, "rebind", testKey(t, "rebind_"), rebind)
	if err != nil {
		t.Fatal(err)
	}
	if rebound.License.Status != "pending" || rebound.License.InstanceID != "" || !rebound.License.LicenseExpiresAt.Equal(*bound.LicenseExpiresAt) {
		t.Fatal("rebind reset commercial dates")
	}
	_, err = a.sync(t, i, issued.License.Domain, issued.License.ID)
	requireCode(t, err, "binding_mismatch")
	_, err = a.s.Issue(ctx, testKey(t, "former-domain_"), issueInput(issued.License.Domain))
	requireCode(t, err, "domain_conflict")
	a.clock.add(12 * time.Hour)
	next := newInstance(t)
	newBound := a.commit(t, next, a.prepare(t, next, rebound))
	if newBound.LicenseID != bound.LicenseID || !newBound.LicenseExpiresAt.Equal(*bound.LicenseExpiresAt) || !newBound.ActivatedAt.Equal(bound.ActivatedAt) {
		t.Fatal("rebind restarted commercial term")
	}
	forged := a.request(t, i, rebound.License.Domain)
	forged.LicenseID = bound.LicenseID
	_, err = a.call(t, i, "sync", forged)
	requireCode(t, err, "binding_mismatch")
}

func TestPostgresConcurrencyAndRollback(t *testing.T) {
	a := testAuthority(t)
	ctx := context.Background()
	t.Run("same idempotency key", func(t *testing.T) {
		key := testKey(t, "concurrent_")
		results := make(chan p.MutationResult, 2)
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for range 2 {
			wg.Go(func() { r, e := a.s.Issue(ctx, key, issueInput("same-key.example.test")); results <- r; errs <- e })
		}
		wg.Wait()
		close(results)
		close(errs)
		for e := range errs {
			if e != nil {
				t.Fatal(e)
			}
		}
		codes, replays := 0, 0
		for r := range results {
			if r.Code != "" {
				codes++
			}
			if r.Replayed {
				replays++
			}
		}
		if codes != 1 || replays != 1 {
			t.Fatal("multiple code disclosures", codes, replays)
		}
	})
	t.Run("domain uniqueness", func(t *testing.T) {
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for range 2 {
			key := testKey(t, "domain_")
			wg.Go(func() { _, err := a.s.Issue(ctx, key, issueInput("exclusive.example.test")); errs <- err })
		}
		wg.Wait()
		close(errs)
		ok, conflict := 0, 0
		for err := range errs {
			if err == nil {
				ok++
			} else {
				requireCode(t, err, "domain_conflict")
				conflict++
			}
		}
		if ok != 1 || conflict != 1 {
			t.Fatal("domain uniqueness failed")
		}
	})
	t.Run("two instances cannot bind", func(t *testing.T) {
		issued := a.issue(t, "racing.example.test")
		one, two := newInstance(t), newInstance(t)
		c1, c2 := a.prepare(t, one, issued), a.prepare(t, two, issued)
		a.dns.mu.Lock()
		a.dns.records[c1.DNSName] = []string{c1.DNSValue, c2.DNSValue}
		a.dns.mu.Unlock()
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for _, item := range []struct {
			i *instance
			c p.Challenge
		}{{one, c1}, {two, c2}} {
			r := a.request(t, item.i, issued.License.Domain)
			r.ReservationID = item.c.ReservationID
			signed, _ := p.SignRequest("commit", r, item.i.private)
			wg.Go(func() { _, err := a.s.Public(ctx, "commit", signed); errs <- err })
		}
		wg.Wait()
		close(errs)
		success := 0
		for err := range errs {
			if err == nil {
				success++
			} else {
				requireCode(t, err, "binding_mismatch")
			}
		}
		if success != 1 {
			t.Fatal("two instances bound")
		}
		list, _ := a.s.List(ctx)
		var record p.LicenseRecord
		for _, l := range list {
			if l.ID == issued.License.ID {
				record = l
			}
		}
		errs = make(chan error, 2)
		for range 2 {
			key := testKey(t, "cas_")
			input := mutation(record.Revision)
			input.Term = &p.Term{Kind: "days", Days: 1}
			wg.Go(func() { _, err := a.s.Mutate(ctx, issued.License.ID, "extend", key, input); errs <- err })
		}
		wg.Wait()
		close(errs)
		success = 0
		for err := range errs {
			if err == nil {
				success++
			} else {
				requireCode(t, err, "revision_conflict")
			}
		}
		if success != 1 {
			t.Fatal("CAS allowed two extensions")
		}
	})
	t.Run("failed transaction leaves no domain or audit", func(t *testing.T) {
		id := testKey(t, "lic_")
		key := testKey(t, "rollback_")
		failure := errors.New("synthetic failure")
		_, _, err := a.store.Run(ctx, key, "digest", func(tx transaction) ([]byte, error) {
			if err := tx.ClaimDomain(ctx, "rollback.example.test", id); err != nil {
				return nil, err
			}
			l := license{Record: p.LicenseRecord{ID: id, Domain: "rollback.example.test"}, CodeHash: testKey(t, "hash_")}
			if err := tx.Save(ctx, l); err != nil {
				return nil, err
			}
			if err := a.s.audit(ctx, tx, l, "synthetic", p.Actor{ID: "test", Name: "test"}, "rollback"); err != nil {
				return nil, err
			}
			return nil, failure
		})
		if !errors.Is(err, failure) {
			t.Fatal(err)
		}
		var count int
		if err = a.store.pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM hl_license_v2.domains WHERE domain='rollback.example.test')+(SELECT count(*) FROM hl_license_v2.audit WHERE license_id=$1)+(SELECT count(*) FROM hl_license_v2.licenses WHERE id=$1)+(SELECT count(*) FROM hl_license_v2.operations WHERE key=$2)", id, key).Scan(&count); err != nil || count != 0 {
			t.Fatal("partial transaction persisted", count, err)
		}
		a.issue(t, "rollback.example.test")
	})
}

func TestPostgresHTTPAndRealHLClient(t *testing.T) {
	a := testAuthority(t)
	server := httptest.NewServer(a.s.Handler())
	defer server.Close()
	input := issueInput("client.example.test")
	raw, _ := json.Marshal(input)
	req, _ := http.NewRequest(http.MethodPost, server.URL+p.ManagementPath, bytes.NewReader(raw))
	req.Header.Set("Idempotency-Key", testKey(t, "http_"))
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("management endpoint is unauthenticated")
	}
	req, _ = http.NewRequest(http.MethodPost, server.URL+p.ManagementPath, bytes.NewReader(raw))
	req.Header.Set("Idempotency-Key", testKey(t, "http_"))
	req.Header.Set("Authorization", "Bearer "+a.token)
	response, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var issued p.MutationResult
	err = json.NewDecoder(response.Body).Decode(&issued)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || issued.Code == "" {
		t.Fatal("HTTP issue failed", response.StatusCode, err)
	}
	config := p.Config{IssuerURL: server.URL, IssuerID: a.s.config.Issuer, Keys: map[string]ed25519.PublicKey{"test-key": a.public}, Domain: input.Domain, InstanceID: "client-integration-01", Directory: t.TempDir(), Now: a.clock.now}
	client, err := p.NewClient(config)
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := client.Prepare(context.Background(), issued.Code)
	if err != nil {
		t.Fatal("real client prepare", err)
	}
	a.dns.set(challenge)
	if err = client.Commit(context.Background()); err != nil {
		t.Fatal("real client commit", err)
	}
	state := client.Status("admin-test")
	if !state.ManagementAllowed || state.Status != "active" || state.LicenseID != issued.License.ID {
		t.Fatal("real client activation state", state)
	}
	a.clock.add(48 * time.Hour)
	if err = client.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if client.Status("admin-test").Status != "expired" {
		t.Fatal("real client did not expire")
	}
	request := mutation(state.Revision)
	request.Term = &p.Term{Kind: "days", Days: 2}
	extended, err := a.s.Mutate(context.Background(), issued.License.ID, "extend", testKey(t, "client_extend_"), request)
	if err != nil {
		t.Fatal(err)
	}
	// The client intentionally coalesces syncs for ten seconds.
	a.clock.add(11 * time.Second)
	if err = client.Sync(context.Background()); err != nil {
		t.Fatal("real client recovery", err)
	}
	state = client.Status("admin-test")
	if !state.ManagementAllowed || state.Status != "active" || state.Revision != extended.License.Revision || state.LicenseID != issued.License.ID {
		t.Fatal("real client recovery mismatch", state)
	}
	reloaded, err := p.NewClient(config)
	if err != nil || !reloaded.Status("admin-test").ManagementAllowed {
		t.Fatal("signed cache reload failed", err)
	}
	revoked, err := a.s.Mutate(context.Background(), issued.License.ID, "revoke", testKey(t, "client_revoke_"), mutation(state.Revision))
	if err != nil {
		t.Fatal(err)
	}
	a.clock.add(11 * time.Second)
	if err = client.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if client.Status("admin-test").Status != "revoked" || client.Status("admin-test").Revision != revoked.License.Revision || client.Status("admin-test").ManagementAllowed {
		t.Fatal("real client ignored revocation")
	}
	req, _ = http.NewRequest(http.MethodGet, server.URL+p.ManagementPath, nil)
	req.Header.Set("Authorization", "Bearer "+a.token)
	response, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	listRaw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	for _, secret := range []string{issued.Code, a.token, "code_hash", "instance_public_key"} {
		if bytes.Contains(listRaw, []byte(secret)) {
			t.Fatal("list leaks secret metadata")
		}
	}
}

func TestPostgresRejectsForgedExpiredAndCrossOperationRequests(t *testing.T) {
	a := testAuthority(t)
	issued := a.issue(t, "negative.example.test")
	i := newInstance(t)
	r := a.request(t, i, issued.License.Domain)
	r.Code = issued.Code
	signed, _ := p.SignRequest("prepare", r, i.private)
	_, err := a.s.Public(context.Background(), "commit", signed)
	requireCode(t, err, "invalid_request")
	signed.Signature = base64.RawURLEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
	_, err = a.s.Public(context.Background(), "prepare", signed)
	requireCode(t, err, "invalid_request")
	r.IssuedAt = a.clock.now().Add(-6 * time.Minute)
	_, err = a.call(t, i, "prepare", r)
	requireCode(t, err, "invalid_request")
	c := a.prepare(t, i, issued)
	a.clock.add(11 * time.Minute)
	commit := a.request(t, i, issued.License.Domain)
	commit.ReservationID = c.ReservationID
	_, err = a.call(t, i, "commit", commit)
	requireCode(t, err, "challenge_expired")
	input := issueInput("invalid.example.test")
	input.Term = p.Term{Kind: "days", Days: 1, Months: 1}
	_, err = a.s.Issue(context.Background(), testKey(t, "invalid_"), input)
	requireCode(t, err, "invalid_request")
	t.Log(fmt.Sprintf("negative authorization tests use only loopback DB and fake DNS"))
}
