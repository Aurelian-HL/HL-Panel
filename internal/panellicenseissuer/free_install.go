package panellicenseissuer

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	p "github.com/hongle/hl-panel/internal/panellicense"
)

const freeInstallPurpose = "free-install-registration"
const freeInstallHTTPPrefix = "/.well-known/hl-panel-license/"

// The installation path has its own signed operations and purpose. A DNS
// challenge can never authorize an HTTP registration (or vice versa).
func (s *Service) freeInstallPublic(ctx context.Context, operation string, r p.Request, signed p.InstanceRequest) (p.Envelope, error) {
	var empty p.Envelope
	if r.Code != "" || r.LicenseID != "" || r.ReservationID != "" {
		return empty, invalid("free installation accepts no license code or reservation")
	}
	if operation == "free-install-prepare" && r.Challenge != nil || operation == "free-install-commit" && r.Challenge == nil {
		return empty, invalid("only free-install-commit requires the signed challenge")
	}
	if operation == "free-install-prepare" {
		return p.Sign(s.prepareFreeInstall(r, signed), s.config.KeyID, s.config.SigningKey)
	}
	digest, err := fingerprint(operation, signed)
	if err != nil {
		return empty, err
	}
	opKey := "public:" + r.RequestID
	if raw, found, err := s.store.Replay(ctx, opKey, digest); err != nil {
		return empty, err
	} else if found {
		var e p.Envelope
		err = json.Unmarshal(raw, &e)
		return e, err
	}
	c, err := s.verifyFreeInstallChallenge(r)
	if err != nil {
		return empty, err
	}
	proofCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = s.config.HTTPVerifier.VerifyHTTPProof(proofCtx, c.Domain, c.HTTPPath, c.HTTPValue)
	cancel()
	if err != nil {
		// DNS, dial and response details can expose internal network state.
		return empty, ErrHTTP
	}
	raw, _, err := s.store.Run(ctx, opKey, digest, func(tx transaction) ([]byte, error) {
		if _, err := s.verifyFreeInstallChallenge(r); err != nil {
			return nil, err
		}
		claims, err := s.commitFreeRegistration(ctx, tx, r, s.verifyFreeInstallChallenge, "HTTP-01")
		if err != nil {
			return nil, err
		}
		e, err := p.Sign(claims, s.config.KeyID, s.config.SigningKey)
		if err != nil {
			return nil, err
		}
		return json.Marshal(e)
	})
	if err != nil {
		return empty, err
	}
	var envelope p.Envelope
	err = json.Unmarshal(raw, &envelope)
	return envelope, err
}

func (s *Service) prepareFreeInstall(r p.Request, signed p.InstanceRequest) p.Challenge {
	nonce := func(label string) string {
		mac := hmac.New(sha256.New, s.config.SigningKey.Seed())
		_, _ = mac.Write([]byte("hl-panel-free-install-prepare-v2\n" + label + "\n" + signed.Payload + "\n" + signed.Signature))
		return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:24])
	}
	key, _ := base64.RawURLEncoding.DecodeString(r.InstancePublicKey)
	id := "free_" + nonce("id")
	return p.Challenge{Purpose: freeInstallPurpose, Version: p.Version, Issuer: s.config.Issuer,
		Product: p.Product, Audience: p.Audience, RequestID: r.RequestID,
		Domain: r.Domain, InstanceID: r.InstanceID,
		InstanceKeyThumbprint: p.Thumbprint(ed25519.PublicKey(key)), ReservationID: id,
		VerificationMethod: "http-01", HTTPPath: freeInstallHTTPPrefix + id,
		HTTPValue: "hl-license-v2=" + nonce("http"),
		IssuedAt:  r.IssuedAt, ExpiresAt: r.IssuedAt.Add(10 * time.Minute)}
}

func validFreeNonce(value string) bool {
	if len(value) != 32 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(raw) == 24 && base64.RawURLEncoding.EncodeToString(raw) == value
}

func (s *Service) verifyFreeInstallChallenge(r p.Request) (p.Challenge, error) {
	var c p.Challenge
	if r.Challenge == nil || p.Verify(*r.Challenge, map[string]ed25519.PublicKey{
		s.config.KeyID: s.config.SigningKey.Public().(ed25519.PublicKey),
	}, &c) != nil {
		return c, invalid("untrusted free installation challenge")
	}
	key, _ := base64.RawURLEncoding.DecodeString(r.InstancePublicKey)
	if c.Purpose != freeInstallPurpose || c.Version != p.Version || c.Issuer != s.config.Issuer ||
		c.Product != p.Product || c.Audience != p.Audience || c.Domain != r.Domain ||
		c.InstanceID != r.InstanceID || c.InstanceKeyThumbprint != p.Thumbprint(ed25519.PublicKey(key)) ||
		len(c.RequestID) < 16 || len(c.RequestID) > 128 ||
		!strings.HasPrefix(c.ReservationID, "free_") || !validFreeNonce(strings.TrimPrefix(c.ReservationID, "free_")) ||
		c.DNSName != "" || c.DNSValue != "" || c.VerificationMethod != "http-01" ||
		c.HTTPPath != freeInstallHTTPPrefix+c.ReservationID ||
		!strings.HasPrefix(c.HTTPValue, "hl-license-v2=") || !validFreeNonce(strings.TrimPrefix(c.HTTPValue, "hl-license-v2=")) {
		return c, ErrBinding
	}
	now := s.now()
	if c.IssuedAt.After(now.Add(5*time.Minute)) || !c.ExpiresAt.After(c.IssuedAt) ||
		c.ExpiresAt.Sub(c.IssuedAt) > 10*time.Minute || !now.Before(c.ExpiresAt) {
		return c, ErrChallengeExpired
	}
	return c, nil
}
