package licensing

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRemoteIssuerActivateAndRenew(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	want := Lease{LicenseID: "lic-1", InstanceID: "instance-1", Domain: "panel.example.com", ActivatedAt: now, ExpiresAt: now.AddDate(0, 1, 0), CheckedAt: now}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload["code"] != "hlp-secret-code" || payload["domain"] != want.Domain || payload["instance_id"] != want.InstanceID {
			t.Fatalf("unexpected request payload: %#v", payload)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(want)
	}))
	defer server.Close()
	issuer, err := NewRemoteIssuer(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := issuer.Activate(context.Background(), "hlp-secret-code", want.Domain, want.InstanceID)
	if err != nil || lease != want {
		t.Fatalf("activate = %#v, %v; want %#v", lease, err, want)
	}
	renewed, err := issuer.Renew(context.Background(), want.LicenseID, "hlp-secret-code", want.Domain, want.InstanceID)
	if err != nil || renewed != want {
		t.Fatalf("renew = %#v, %v; want %#v", renewed, err, want)
	}
}

func TestRemoteIssuerMapsLicenseErrorsWithoutLeakingCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"license_domain_mismatch","message":"授权码无效"}}`))
	}))
	defer server.Close()
	issuer, err := NewRemoteIssuer(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = issuer.Activate(context.Background(), "hlp-secret-code", "wrong.example.com", "instance-1")
	if !errors.Is(err, ErrDomainMismatch) {
		t.Fatalf("error = %v, want ErrDomainMismatch", err)
	}
	if err != nil && containsSecret(err.Error(), "hlp-secret-code") {
		t.Fatal("remote error leaked the license code")
	}
}

func TestRemoteIssuerRejectsNonLoopbackHTTP(t *testing.T) {
	for _, raw := range []string{"http://issuer.example.com", "http://192.0.2.10:8080", "http://user:pass@127.0.0.1:8080"} {
		if _, err := NewRemoteIssuer(raw, time.Second); err == nil {
			t.Fatalf("issuer URL %q was accepted", raw)
		}
	}
	for _, raw := range []string{"http://127.0.0.1:8080", "http://[::1]:8080", "http://localhost:8080"} {
		if _, err := NewRemoteIssuer(raw, time.Second); err != nil {
			t.Fatalf("loopback issuer URL %q was rejected: %v", raw, err)
		}
	}
}

func containsSecret(value, secret string) bool {
	return len(secret) > 0 && len(value) >= len(secret) && stringContains(value, secret)
}

func stringContains(value, part string) bool {
	for i := 0; i+len(part) <= len(value); i++ {
		if value[i:i+len(part)] == part {
			return true
		}
	}
	return false
}
