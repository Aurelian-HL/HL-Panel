package panellicenseissuer

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	p "github.com/hongle/hl-panel/internal/panellicense"
)

type redemptionVerifierFunc func(context.Context, string, string, string) ([]string, error)

func (f redemptionVerifierFunc) VerifyRedemptionProof(ctx context.Context, domain, path, value string) ([]string, error) {
	return f(ctx, domain, path, value)
}

func stockAuthority(t *testing.T) *authority {
	t.Helper()
	a := testAuthority(t)
	a.s.config.ExportKey = make([]byte, 32)
	if _, err := rand.Read(a.s.config.ExportKey); err != nil {
		t.Fatal(err)
	}
	a.s.config.RedemptionVerifier = redemptionVerifierFunc(func(context.Context, string, string, string) ([]string, error) {
		return []string{"8.8.8.8"}, nil
	})
	return a
}

func stockActor() p.StockActionRequest {
	return p.StockActionRequest{Actor: p.Actor{ID: "stock-test", Name: "Stock Test"}, Reason: "isolated stock acceptance"}
}

func (a *authority) stock(t *testing.T, product string, count int) (p.StockBatch, []string) {
	t.Helper()
	actor := stockActor()
	b, err := a.s.CreateStockBatch(context.Background(), testKey(t, "batch_"), p.StockBatchRequest{ProductID: product, Count: count, Actor: actor.Actor, Reason: actor.Reason})
	if err != nil {
		t.Fatal(err)
	}
	codes, err := a.s.ExportStockBatch(context.Background(), b.ID, actor)
	if err != nil || len(codes) != count {
		t.Fatal("inventory export failed", err)
	}
	return b, codes
}

func (a *authority) redemption(t *testing.T, i *instance, domain, code string) p.Envelope {
	t.Helper()
	r := a.request(t, i, domain)
	r.Code = code
	e, err := a.call(t, i, "redeem-prepare", r)
	if err != nil {
		t.Fatal(err)
	}
	var attempt p.RedemptionAttempt
	if err := p.Verify(e, map[string]ed25519.PublicKey{"test-key": a.public}, &attempt); err != nil || !p.ValidRedemptionProof(attempt) || attempt.CodeHash != hash(code) {
		t.Fatal("invalid redemption receipt", err)
	}
	return e
}

func (a *authority) redeem(t *testing.T, i *instance, domain string, receipt p.Envelope) (p.Claims, error) {
	t.Helper()
	r := a.request(t, i, domain)
	r.Challenge = &receipt
	e, err := a.call(t, i, "redeem-commit", r)
	if err != nil {
		return p.Claims{}, err
	}
	var c p.Claims
	err = p.Verify(e, map[string]ed25519.PublicKey{"test-key": a.public}, &c)
	return c, err
}

func (a *authority) unused(t *testing.T, code string) {
	t.Helper()
	c, err := a.store.StockCode(context.Background(), hash(code))
	if err != nil || c.Redemption != nil {
		t.Fatal("refused redemption consumed a code", err)
	}
}

func TestStockExportLifecycleAndSecretStorage(t *testing.T) {
	a := stockAuthority(t)
	ctx := context.Background()
	actor := stockActor()
	input := p.StockBatchRequest{ProductID: "monthly", Count: 2, Actor: actor.Actor, Reason: actor.Reason}
	key := testKey(t, "stock_")
	first, err := a.s.CreateStockBatch(ctx, key, input)
	if err != nil {
		t.Fatal(err)
	}
	again, err := a.s.CreateStockBatch(ctx, key, input)
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatal("stock creation not idempotent", err)
	}
	input.Count++
	_, err = a.s.CreateStockBatch(ctx, key, input)
	requireCode(t, err, "idempotency_conflict")
	codes, err := a.s.ExportStockBatch(ctx, first.ID, actor)
	if err != nil || len(codes) != 2 || codes[0] == codes[1] {
		t.Fatal("invalid stock export", err)
	}
	retry, err := a.s.ExportStockBatch(ctx, first.ID, actor)
	if err != nil || !reflect.DeepEqual(codes, retry) {
		t.Fatal("lost download recovery changed codes", err)
	}
	var stored string
	err = a.store.pool.QueryRow(ctx, "SELECT coalesce((SELECT string_agg(document::text,'') FROM hl_license_v2.stock_batches),'') || coalesce((SELECT string_agg(document::text,'') FROM hl_license_v2.stock_audit),'') || coalesce((SELECT string_agg(convert_from(result,'UTF8'),'') FROM hl_license_v2.operations),'') || coalesce((SELECT string_agg(row_to_json(c)::text,'') FROM hl_license_v2.stock_codes c),'')").Scan(&stored)
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range codes {
		if len(code) != 37 || !strings.HasPrefix(code, "hlr1_") || strings.Contains(stored, code) {
			t.Fatal("invalid format or plaintext code retained in metadata/replay/audit")
		}
	}
	_, err = a.s.StockAction(ctx, first.ID, "confirm-export", testKey(t, "confirm_"), actor)
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.s.ExportStockBatch(ctx, first.ID, actor)
	requireCode(t, err, "export_closed")
	var empty bool
	if err = a.store.pool.QueryRow(ctx, "SELECT export_ciphertext IS NULL FROM hl_license_v2.stock_batches WHERE id=$1", first.ID).Scan(&empty); err != nil || !empty {
		t.Fatal("confirmed export material retained", err)
	}
	i := newInstance(t)
	receipt := a.redemption(t, i, "confirmed.example.test", codes[0])
	if _, err = a.redeem(t, i, "confirmed.example.test", receipt); err != nil {
		t.Fatal("confirmation invalidated delivered code", err)
	}
	old, expiredCodes := a.stock(t, "yearly", 1)
	a.clock.add(25 * time.Hour)
	if err := a.store.PurgeExpiredExports(ctx, a.clock.now()); err != nil {
		t.Fatal(err)
	}
	if err = a.store.pool.QueryRow(ctx, "SELECT export_ciphertext IS NULL FROM hl_license_v2.stock_batches WHERE id=$1", old.ID).Scan(&empty); err != nil || !empty {
		t.Fatal("expired delivery material retained", err)
	}
	_, err = a.s.ExportStockBatch(ctx, old.ID, actor)
	requireCode(t, err, "export_closed")
	receipt = a.redemption(t, i, "expired-export.example.test", expiredCodes[0])
	if _, err = a.redeem(t, i, "expired-export.example.test", receipt); err != nil {
		t.Fatal("export expiry invalidated delivered code", err)
	}
}

func TestStockConcurrentSingleUseAndRenewal(t *testing.T) {
	a := stockAuthority(t)
	batch, codes := a.stock(t, "monthly", 4)
	i, other := newInstance(t), newInstance(t)
	domain := "concurrent.example.test"
	receipt := a.redemption(t, i, domain, codes[0])
	rival := a.redemption(t, other, "rival.example.test", codes[0])
	const calls = 8
	requests := make([]p.InstanceRequest, calls)
	for index := range requests {
		r := a.request(t, i, domain)
		r.Challenge = &receipt
		var err error
		requests[index], err = p.SignRequest("redeem-commit", r, i.private)
		if err != nil {
			t.Fatal(err)
		}
	}
	results := make(chan error, calls)
	for _, r := range requests {
		go func(r p.InstanceRequest) {
			_, err := a.s.Public(context.Background(), "redeem-commit", r)
			results <- err
		}(r)
	}
	for range requests {
		if err := <-results; err != nil {
			t.Fatal("same-instance recovery failed", err)
		}
	}
	claims, err := a.redeem(t, i, domain, receipt)
	end, _ := addTerm(a.clock.now(), batch.Term)
	if err != nil || claims.Revision != 1 || claims.LicenseExpiresAt == nil || !claims.LicenseExpiresAt.Equal(*end) {
		t.Fatal("same code applied more than once", err)
	}
	_, err = a.redeem(t, other, "rival.example.test", rival)
	requireCode(t, err, "code_already_redeemed")
	// Distinct codes on one domain must add, even when committed concurrently.
	results = make(chan error, 2)
	for _, code := range codes[1:3] {
		e := a.redemption(t, i, domain, code)
		r := a.request(t, i, domain)
		r.Challenge = &e
		signed, _ := p.SignRequest("redeem-commit", r, i.private)
		go func() { _, err := a.s.Public(context.Background(), "redeem-commit", signed); results <- err }()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal("concurrent renewal failed", err)
		}
	}
	current, err := a.sync(t, i, domain, claims.LicenseID)
	end, _ = addTerm(*end, batch.Term)
	end, _ = addTerm(*end, batch.Term)
	if err != nil || current.Revision != 3 || !current.LicenseExpiresAt.Equal(*end) {
		t.Fatal("lost concurrent renewal", err)
	}
	redemptions, err := a.store.Redemptions(context.Background(), batch.ID)
	if err != nil || len(redemptions) != 3 || redemptions[0].IPSource != "https-verified-dns" || !reflect.DeepEqual(redemptions[0].VerifiedIPs, []string{"8.8.8.8"}) {
		t.Fatal("missing durable redemption identity or verified IP", err)
	}
	a.requireLicenseCounts(t, 1, 1, 0, 3)
	a.clock.add(100 * 24 * time.Hour)
	late := a.redemption(t, i, domain, codes[3])
	current, err = a.redeem(t, i, domain, late)
	expected, _ := addTerm(a.clock.now(), batch.Term)
	if err != nil || !current.LicenseExpiresAt.Equal(*expected) {
		t.Fatal("expired renewal did not start from now", err)
	}
}

func TestStockRefusedProofExpiryAndPermanentDoNotConsume(t *testing.T) {
	a := stockAuthority(t)
	i := newInstance(t)
	domain := "proof-failure.example.test"
	_, codes := a.stock(t, "monthly", 3)
	receipt := a.redemption(t, i, domain, codes[0])
	a.s.config.RedemptionVerifier = redemptionVerifierFunc(func(context.Context, string, string, string) ([]string, error) {
		return nil, errors.New("isolated TLS failure")
	})
	_, err := a.redeem(t, i, domain, receipt)
	requireCode(t, err, "https_not_verified")
	a.unused(t, codes[0])
	a.clock.add(11 * time.Minute)
	_, err = a.redeem(t, i, domain, receipt)
	requireCode(t, err, "challenge_expired")
	a.unused(t, codes[0])
	a.s.config.RedemptionVerifier = redemptionVerifierFunc(func(context.Context, string, string, string) ([]string, error) { return []string{"8.8.8.8"}, nil })
	_, permanent := a.stock(t, "perpetual", 1)
	claims, err := a.redeem(t, i, domain, a.redemption(t, i, domain, permanent[0]))
	if err != nil || claims.LicenseExpiresAt != nil || claims.Term.Kind != "perpetual" {
		t.Fatal("permanent activation failed", err)
	}
	_, err = a.redeem(t, i, domain, a.redemption(t, i, domain, codes[1]))
	requireCode(t, err, "operation_not_allowed")
	a.unused(t, codes[1])
	a.requireLicenseCounts(t, 1, 1, 0, 1)
}

func TestStockFreeUpgradeAndRecoveryUseCurrentRights(t *testing.T) {
	a := stockAuthority(t)
	i := newInstance(t)
	domain := "upgrade.example.test"
	free, challenge := a.prepareFree(t, i, domain)
	a.dns.set(challenge)
	envelope, err := a.call(t, i, "free-commit", a.freeRequest(t, i, domain, free))
	if err != nil {
		t.Fatal(err)
	}
	before := a.freeClaims(t, envelope)
	_, codes := a.stock(t, "monthly", 1)
	receipt := a.redemption(t, i, domain, codes[0])
	upgraded, err := a.redeem(t, i, domain, receipt)
	if err != nil || upgraded.LicenseID != before.LicenseID || upgraded.Tier != p.TierLicensed || upgraded.Revision != before.Revision+1 || upgraded.Limits != (p.Limits{}) {
		t.Fatal("free upgrade replaced identity or retained free quota", err)
	}
	a.clock.add(48 * time.Hour)
	a.s.config.RedemptionVerifier = redemptionVerifierFunc(func(context.Context, string, string, string) ([]string, error) {
		t.Error("already redeemed recovery should not need an expired proof")
		return nil, ErrHTTP
	})
	recovered, err := a.redeem(t, i, domain, receipt)
	if err != nil || recovered.Revision != upgraded.Revision || recovered.RequestID == upgraded.RequestID {
		t.Fatal("expired successful receipt could not recover", err)
	}
	_, err = a.s.Mutate(context.Background(), upgraded.LicenseID, "rebind", testKey(t, "rebind_"), mutation(upgraded.Revision))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.redeem(t, i, domain, receipt)
	requireCode(t, err, "binding_mismatch")
}

func TestStockRecoveryDoesNotResurrectRevocation(t *testing.T) {
	a := stockAuthority(t)
	i := newInstance(t)
	domain := "revoked.example.test"
	_, codes := a.stock(t, "monthly", 2)
	receipt := a.redemption(t, i, domain, codes[0])
	active, err := a.redeem(t, i, domain, receipt)
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.s.Mutate(context.Background(), active.LicenseID, "revoke", testKey(t, "revoke_"), mutation(active.Revision))
	if err != nil {
		t.Fatal(err)
	}
	current, err := a.redeem(t, i, domain, receipt)
	if err != nil || current.Status != "revoked" || current.Revision != active.Revision+1 {
		t.Fatal("receipt resurrected old rights", err)
	}
	_, err = a.redeem(t, i, domain, a.redemption(t, i, domain, codes[1]))
	requireCode(t, err, "license_revoked")
	a.unused(t, codes[1])
}

func TestStockDisableWinsDuringExternalProof(t *testing.T) {
	a := stockAuthority(t)
	batch, codes := a.stock(t, "monthly", 1)
	i := newInstance(t)
	domain := "disable-race.example.test"
	receipt := a.redemption(t, i, domain, codes[0])
	entered, release := make(chan struct{}), make(chan struct{})
	a.s.config.RedemptionVerifier = redemptionVerifierFunc(func(ctx context.Context, _, _, _ string) ([]string, error) {
		close(entered)
		select {
		case <-release:
			return []string{"8.8.8.8"}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	r := a.request(t, i, domain)
	r.Challenge = &receipt
	signed, _ := p.SignRequest("redeem-commit", r, i.private)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := a.s.Public(ctx, "redeem-commit", signed); done <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("proof not reached")
	}
	disableCtx, disableCancel := context.WithTimeout(context.Background(), time.Second)
	_, err := a.s.StockAction(disableCtx, batch.ID, "disable", testKey(t, "disable_"), stockActor())
	disableCancel()
	close(release)
	if err != nil {
		t.Fatal("external proof held database lock", err)
	}
	requireCode(t, <-done, "code_disabled")
	a.unused(t, codes[0])
	a.requireLicenseCounts(t, 0, 0, 0, 0)
}

func TestStockClientRecoversCommittedResponseAfterRestart(t *testing.T) {
	a := stockAuthority(t)
	batch, codes := a.stock(t, "monthly", 1)
	var drop atomic.Bool
	drop.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == p.PublicPath+"/redemption/commit" && drop.Swap(false) {
			recorder := httptest.NewRecorder()
			a.s.Handler().ServeHTTP(recorder, r)
			if recorder.Code != 200 {
				t.Error("synthetic committed request failed", recorder.Code)
			}
			http.Error(w, "synthetic lost success response", 502)
			return
		}
		a.s.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()
	config := p.Config{IssuerURL: server.URL, IssuerID: a.s.config.Issuer, Keys: map[string]ed25519.PublicKey{"test-key": a.public}, Domain: "restart.example.test", InstanceID: "hlp-stock-integration", Directory: t.TempDir(), Now: a.clock.now}
	client, err := p.NewClient(config)
	if err != nil {
		t.Fatal(err)
	}
	var proofs atomic.Int32
	a.s.config.RedemptionVerifier = redemptionVerifierFunc(func(_ context.Context, domain, path, value string) ([]string, error) {
		proofs.Add(1)
		proof := httptest.NewRecorder()
		client.ServeProof(proof, httptest.NewRequest(http.MethodGet, "https://"+domain+path, nil))
		body, _ := io.ReadAll(proof.Result().Body)
		if proof.Code != 200 || string(body) != value {
			return nil, ErrHTTP
		}
		return []string{"8.8.8.8"}, nil
	})
	if err = client.Redeem(context.Background(), codes[0]); err == nil || !client.Status("admin").RedemptionPending {
		t.Fatal("lost success not retained as pending", err)
	}
	raw, err := os.ReadFile(filepath.Join(config.Directory, "state.json"))
	if err != nil || strings.Contains(string(raw), codes[0]) {
		t.Fatal("missing receipt or plaintext purchased code persisted", err)
	}
	if _, err = client.Prepare(context.Background(), "hl2_legacy-code-test"); !errors.Is(err, p.ErrRedemptionPending) {
		t.Fatal("legacy flow overwrote pending redemption", err)
	}
	if _, err = client.RegisterFree(context.Background()); !errors.Is(err, p.ErrRedemptionPending) {
		t.Fatal("free flow overwrote pending redemption", err)
	}
	client, err = p.NewClient(config)
	if err != nil || !client.Status("admin").RedemptionPending {
		t.Fatal("receipt lost across restart", err)
	}
	a.clock.add(24 * time.Hour)
	if err = client.ResumeRedemption(context.Background()); err != nil {
		t.Fatal(err)
	}
	status := client.Status("admin")
	if status.RedemptionPending || status.Revision != 1 || !status.ManagementAllowed || status.Tier != p.TierLicensed || proofs.Load() != 1 {
		t.Fatal("recovery extended twice or reran expired proof")
	}
	items, err := a.store.Redemptions(context.Background(), batch.ID)
	if err != nil || len(items) != 1 {
		t.Fatal("recovery duplicated redemption", err)
	}
}

func TestStockManagementHTTPRequiresAuthorityAndIdempotency(t *testing.T) {
	a := stockAuthority(t)
	actor := stockActor()
	raw, _ := json.Marshal(p.StockBatchRequest{ProductID: "monthly", Count: 1, Actor: actor.Actor, Reason: actor.Reason})
	call := func(method, path, token, key string, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(string(body)))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		a.s.Handler().ServeHTTP(w, r)
		return w
	}
	if w := call(http.MethodPost, p.StockPath, "", testKey(t, "stock_"), raw); w.Code != 401 {
		t.Fatal("anonymous inventory mutation accepted")
	}
	if w := call(http.MethodPost, p.StockPath, a.token, "", raw); w.Code != 400 {
		t.Fatal("mutation without idempotency key accepted", w.Code)
	}
	w := call(http.MethodPost, p.StockPath, a.token, testKey(t, "stock_"), raw)
	var b p.StockBatch
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &b) != nil || b.ID == "" || strings.Contains(w.Body.String(), "hlr1_") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("unsafe creation response", w.Code)
	}
	for _, action := range []string{"export", "redemptions", "disable", "confirm-export"} {
		method := http.MethodPost
		if action == "redemptions" {
			method = http.MethodGet
		}
		w = call(method, p.StockPath+"/"+b.ID+"/"+action, "bad", testKey(t, "stock_"), raw)
		if w.Code != 401 || strings.Contains(w.Body.String(), "hlr1_") {
			t.Fatal("stock endpoint leaked inventory", action)
		}
	}
	if w = call(http.MethodGet, p.StockPath+"?offset=-1", a.token, "", nil); w.Code != 400 {
		t.Fatal("negative pagination accepted")
	}
}
