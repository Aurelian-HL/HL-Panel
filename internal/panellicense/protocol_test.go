package panellicense

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"
)

func TestRequestProofBindsOperationIdentityTimeAndExactBytes(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	r := Request{Version: Version, Product: Product, Audience: Audience, RequestID: "random-nonce-128-bits-minimum", IssuedAt: now, Domain: "panel.example.test", InstanceID: "hlp-test-instance", InstancePublicKey: base64.RawURLEncoding.EncodeToString(pub), Code: "secret-code-for-test"}
	signed, err := SignRequest("prepare", r, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = VerifyRequest("prepare", signed, now); err != nil {
		t.Fatal(err)
	}
	if _, err = VerifyRequest("commit", signed, now); err == nil {
		t.Fatal("operation substitution accepted")
	}
	if _, err = VerifyRequest("prepare", signed, now.Add(6*time.Minute)); err == nil {
		t.Fatal("expired request accepted")
	}
	for _, domain := range []string{"192.0.2.1", "https://panel.test", "panel.test:443", "panel..test", "panel_test.test", "-panel.test", "中文.test"} {
		if _, err = NormalizeDomain(domain); err == nil {
			t.Fatalf("invalid domain: %s", domain)
		}
	}
	for _, payload := range []string{`{"unknown":true}`, `{} {}`, `{}\nnull`} {
		encoded := base64.RawURLEncoding.EncodeToString([]byte(payload))
		if err = Decode(encoded, &Request{}); err == nil {
			t.Fatalf("ambiguous payload accepted: %s", payload)
		}
	}
}
