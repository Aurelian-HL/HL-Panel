package panellicense

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"
)

func freeClientLimits() Limits { return Limits{DeviceGroups: 1, Rules: 10} }

func TestFreeRegistrationSignedProofRestartAndDNSRetry(t *testing.T) {
	f := newSignedFixture(t)
	var original Envelope
	commits := 0
	f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		op := map[string]string{PublicPath + "/free/prepare": "free-prepare", PublicPath + "/free/commit": "free-commit"}[r.URL.Path]
		var signed InstanceRequest
		if err := json.NewDecoder(r.Body).Decode(&signed); err != nil {
			t.Error(err)
			http.Error(w, "bad", 400)
			return
		}
		req, err := VerifyRequest(op, signed, f.now)
		if err != nil {
			t.Error(err)
			http.Error(w, "bad signature", 401)
			return
		}
		if req.Code != "" || req.LicenseID != "" || req.ReservationID != "" {
			t.Error("free registration leaked paid fields")
		}
		var value any
		if op == "free-prepare" {
			if req.Challenge != nil {
				t.Error("prepare carried challenge")
			}
			value = Challenge{Version: Version, Issuer: f.client.config.IssuerID, Product: Product, Audience: Audience, RequestID: req.RequestID, Domain: req.Domain, InstanceID: req.InstanceID, InstanceKeyThumbprint: Thumbprint(f.client.key.Public().(ed25519.PublicKey)), Purpose: "free-registration", ReservationID: "free-test", DNSName: "_hl-license." + req.Domain, DNSValue: "hl-license-v2=free-proof", IssuedAt: req.IssuedAt, ExpiresAt: req.IssuedAt.Add(10 * time.Minute)}
		} else if op == "free-commit" {
			commits++
			if req.Challenge == nil || !reflect.DeepEqual(*req.Challenge, original) {
				t.Error("commit did not retain complete original signed proof")
			}
			if commits == 1 {
				http.Error(w, "DNS not propagated", 409)
				return
			}
			claims := f.claims(req.RequestID)
			claims.Tier, claims.Limits = TierFree, freeClientLimits()
			claims.Term, claims.LicenseExpiresAt = Term{Kind: "perpetual"}, nil
			value = claims
		} else {
			t.Error("wrong route")
			http.Error(w, "route", 404)
			return
		}
		envelope, err := Sign(value, "test", f.key)
		if err != nil {
			t.Error(err)
			return
		}
		if op == "free-prepare" {
			original = envelope
		}
		_ = json.NewEncoder(w).Encode(envelope)
	})
	proof, err := f.client.RegisterFree(context.Background())
	if err != nil || proof.Purpose != "free-registration" {
		t.Fatalf("prepare: %+v %v", proof, err)
	}
	if f.client.Status("").ManagementAllowed {
		t.Fatal("unverified DNS granted management")
	}
	reloaded, err := NewClient(f.client.config)
	if err != nil {
		t.Fatal(err)
	}
	if err = reloaded.Commit(context.Background()); err == nil {
		t.Fatal("unverified DNS accepted")
	}
	if err = reloaded.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	status := reloaded.Status("admin")
	if !status.ManagementAllowed || status.Tier != TierFree || status.Limits == nil || *status.Limits != freeClientLimits() || status.ReminderDue {
		t.Fatalf("free status: %+v", status)
	}
	if _, err = reloaded.RegisterFree(context.Background()); err == nil {
		t.Fatal("existing license re-registered")
	}
}

func TestFreeRegistrationRejectsPaidChallengeAndExpiredLicenseDowngrade(t *testing.T) {
	f := newSignedFixture(t)
	f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var signed InstanceRequest
		_ = json.NewDecoder(r.Body).Decode(&signed)
		req, err := VerifyRequest("free-prepare", signed, f.now)
		if err != nil {
			t.Error(err)
			return
		}
		proof := Challenge{Version: Version, Issuer: f.client.config.IssuerID, Product: Product, Audience: Audience, RequestID: req.RequestID, Domain: req.Domain, InstanceID: req.InstanceID, InstanceKeyThumbprint: Thumbprint(f.client.key.Public().(ed25519.PublicKey)), ReservationID: "paid-proof", DNSName: "_hl-license." + req.Domain, DNSValue: "hl-license-v2=proof", IssuedAt: f.now, ExpiresAt: f.now.Add(10 * time.Minute)}
		envelope, _ := Sign(proof, "test", f.key)
		_ = json.NewEncoder(w).Encode(envelope)
	})
	if _, err := f.client.RegisterFree(context.Background()); err == nil {
		t.Fatal("paid challenge accepted as free")
	}
	f.server.Config.Handler = http.HandlerFunc(f.serve)
	f.activate()
	f.now = f.expires.Add(time.Second)
	before := f.calls.Load()
	if _, err := f.client.RegisterFree(context.Background()); err == nil || f.calls.Load() != before {
		t.Fatal("expired paid license silently downgraded or contacted free issuer")
	}
}

func TestClientRejectsUnsignedOrMismatchedEntitlements(t *testing.T) {
	for _, tc := range []struct {
		tier   string
		limits Limits
	}{{"", Limits{}}, {"unknown", Limits{}}, {TierFree, Limits{}}, {TierFree, Limits{DeviceGroups: 1, Rules: 11}}, {TierLicensed, freeClientLimits()}} {
		t.Run(fmt.Sprintf("%s-%d", tc.tier, tc.limits.Rules), func(t *testing.T) {
			f := newSignedFixture(t)
			claims := f.claims("request")
			claims.Tier, claims.Limits = tc.tier, tc.limits
			if err := f.accept(claims); err == nil || f.client.Status("").ManagementAllowed {
				t.Fatal("invalid entitlements granted management")
			}
		})
	}
}
