package panellicenseissuer

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	p "github.com/hongle/hl-panel/internal/panellicense"
	"strings"
	"time"
)

const freePurpose = "free-registration"

// Preparing a free registration proves no ownership. It creates only a signed,
// short-lived deterministic challenge, never a database write or domain claim.
func (s *Service) freePublic(ctx context.Context, operation string, r p.Request, signed p.InstanceRequest) (p.Envelope, error) {
	var empty p.Envelope
	if r.Code != "" || r.LicenseID != "" || r.ReservationID != "" {
		return empty, invalid("free registration accepts no license code or reservation")
	}
	if operation == "free-prepare" && r.Challenge != nil || operation == "free-commit" && r.Challenge == nil {
		return empty, invalid("only free-commit requires the signed challenge")
	}
	if operation == "free-prepare" {
		return p.Sign(s.prepareFree(r, signed), s.config.KeyID, s.config.SigningKey)
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
	if operation == "free-commit" {
		c, err := s.verifyFreeChallenge(r)
		if err != nil {
			return empty, err
		}
		dnsCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		records, lookupErr := s.config.Resolver.LookupTXT(dnsCtx, c.DNSName)
		cancel()
		found := false
		for _, value := range records {
			if value == c.DNSValue {
				found = true
			}
		}
		if lookupErr != nil || !found {
			return empty, ErrDNS
		}
	}
	raw, _, err := s.store.Run(ctx, opKey, digest, func(tx transaction) ([]byte, error) {
		// DNS runs outside the transaction. Expiry and domain ownership are
		// checked again under the domain/license locks before creating state.
		if _, err := s.verifyFreeChallenge(r); err != nil {
			return nil, err
		}
		value, err := s.commitFree(ctx, tx, r)
		if err != nil {
			return nil, err
		}
		e, err := p.Sign(value, s.config.KeyID, s.config.SigningKey)
		if err != nil {
			return nil, err
		}
		return json.Marshal(e)
	})
	if err != nil {
		return empty, err
	}
	var e p.Envelope
	err = json.Unmarshal(raw, &e)
	return e, err
}

func (s *Service) prepareFree(r p.Request, signed p.InstanceRequest) p.Challenge {
	// Separate derivation labels prevent cross-use of identity and DNS nonces.
	// Complete signed bytes bind retries without keeping anonymous rows forever.
	nonce := func(label string) string {
		mac := hmac.New(sha256.New, s.config.SigningKey.Seed())
		_, _ = mac.Write([]byte("hl-panel-free-prepare-v2\n" + label + "\n" + signed.Payload + "\n" + signed.Signature))
		return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:24])
	}
	key, _ := base64.RawURLEncoding.DecodeString(r.InstancePublicKey)
	now := r.IssuedAt
	return p.Challenge{Purpose: freePurpose, Version: p.Version, Issuer: s.config.Issuer,
		Product: p.Product, Audience: p.Audience, RequestID: r.RequestID,
		Domain: r.Domain, InstanceID: r.InstanceID,
		InstanceKeyThumbprint: p.Thumbprint(ed25519.PublicKey(key)), ReservationID: "free_" + nonce("id"),
		DNSName: "_hl-license." + r.Domain, DNSValue: "hl-license-v2=" + nonce("dns"),
		IssuedAt: now, ExpiresAt: now.Add(10 * time.Minute)}
}

func (s *Service) verifyFreeChallenge(r p.Request) (p.Challenge, error) {
	var c p.Challenge
	if r.Challenge == nil || p.Verify(*r.Challenge, map[string]ed25519.PublicKey{
		s.config.KeyID: s.config.SigningKey.Public().(ed25519.PublicKey),
	}, &c) != nil {
		return c, invalid("untrusted free registration challenge")
	}
	key, _ := base64.RawURLEncoding.DecodeString(r.InstancePublicKey)
	if c.Purpose != freePurpose || c.Version != p.Version || c.Issuer != s.config.Issuer ||
		c.Product != p.Product || c.Audience != p.Audience || c.Domain != r.Domain ||
		c.InstanceID != r.InstanceID || c.InstanceKeyThumbprint != p.Thumbprint(ed25519.PublicKey(key)) ||
		len(c.RequestID) < 16 || len(c.RequestID) > 128 ||
		!strings.HasPrefix(c.ReservationID, "free_") || len(c.ReservationID) != 37 ||
		c.DNSName != "_hl-license."+r.Domain || !strings.HasPrefix(c.DNSValue, "hl-license-v2=") || len(c.DNSValue) != 46 {
		return c, ErrBinding
	}
	now := s.now()
	if c.IssuedAt.After(now.Add(5*time.Minute)) || !c.ExpiresAt.After(c.IssuedAt) ||
		c.ExpiresAt.Sub(c.IssuedAt) > 10*time.Minute || !now.Before(c.ExpiresAt) {
		return c, ErrChallengeExpired
	}
	return c, nil
}

func (s *Service) commitFree(ctx context.Context, tx transaction, r p.Request) (p.Claims, error) {
	var empty p.Claims
	l, err := tx.ByDomain(ctx, r.Domain)
	if _, challengeErr := s.verifyFreeChallenge(r); challengeErr != nil {
		return empty, challengeErr
	}
	if err == nil {
		if err = normalizeStoredEntitlements(&l.Record); err != nil {
			return empty, err
		}
		if l.Record.RevokedAt != nil {
			return empty, ErrRevoked
		}
		// A formal license can only change tier through an audited management
		// operation; expiration never enables a new free registration.
		if l.Record.Tier != p.TierFree || !matchBinding(l, r) || l.Record.ActivatedAt == nil || status(l.Record, s.now()) != "active" {
			return empty, ErrDomainConflict
		}
		if err = s.audit(ctx, tx, l, "free-register-recover", p.Actor{ID: r.InstanceID, Name: "HL instance"}, "DNS ownership and existing free binding verified"); err != nil {
			return empty, err
		}
		return s.claims(l, r.RequestID), nil
	}
	if !errors.Is(err, ErrNotFound) {
		return empty, err
	}
	id, err := randomID("lic_")
	if err != nil {
		return empty, err
	}
	if err = tx.ClaimDomain(ctx, r.Domain, id); err != nil {
		return empty, err
	}
	// Free registration has no activation code. Reserve an unguessable hash
	// for the existing unique storage column without delivering a free code.
	secret, err := randomID("free_identity_")
	if err != nil {
		return empty, err
	}
	limits, _ := p.LimitsForTier(p.TierFree)
	now := s.now()
	l = license{Record: p.LicenseRecord{ID: id, Domain: r.Domain, Tier: p.TierFree,
		Limits: limits, Term: p.Term{Kind: "perpetual"}, Revision: 1, Status: "active",
		InstanceID: r.InstanceID, CreatedAt: now, ActivatedAt: &now},
		CodeHash: hash(secret), InstancePublicKey: r.InstancePublicKey}
	if err = tx.Save(ctx, l); err != nil {
		return empty, err
	}
	if err = s.audit(ctx, tx, l, "free-register", p.Actor{ID: r.InstanceID, Name: "HL instance"}, "Free domain self-registration verified by DNS and instance signature"); err != nil {
		return empty, err
	}
	return s.claims(l, r.RequestID), nil
}
