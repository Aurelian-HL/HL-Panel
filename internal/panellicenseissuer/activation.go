package panellicenseissuer

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	p "github.com/hongle/hl-panel/internal/panellicense"
	"time"
)

func (s *Service) Public(ctx context.Context, operation string, signed p.InstanceRequest) (p.Envelope, error) {
	var empty p.Envelope
	if operation != "prepare" && operation != "commit" && operation != "sync" && operation != "free-prepare" && operation != "free-commit" && operation != "free-install-prepare" && operation != "free-install-commit" {
		return empty, invalid("unknown activation operation")
	}
	r, err := p.VerifyRequest(operation, signed, s.now())
	if err != nil {
		return empty, invalid("invalid signed instance request")
	}
	if operation == "free-prepare" || operation == "free-commit" {
		return s.freePublic(ctx, operation, r, signed)
	}
	if operation == "free-install-prepare" || operation == "free-install-commit" {
		return s.freeInstallPublic(ctx, operation, r, signed)
	}
	if r.Challenge != nil {
		return empty, invalid("unexpected free registration challenge")
	}
	switch operation {
	case "prepare":
		if len(r.Code) < 20 || len(r.Code) > 128 || r.ReservationID != "" || r.LicenseID != "" {
			return empty, invalid("prepare requires only a license code")
		}
	case "commit":
		if r.ReservationID == "" || len(r.ReservationID) > 128 || r.Code != "" || r.LicenseID != "" {
			return empty, invalid("commit requires only reservation_id")
		}
	case "sync":
		if r.LicenseID == "" || len(r.LicenseID) > 128 || r.Code != "" || r.ReservationID != "" {
			return empty, invalid("sync requires only license_id")
		}
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
	// Resolve DNS outside the database transaction; recheck the reservation under
	// the license lock before committing so rotate/rebind/revoke cannot race it.
	if operation == "commit" {
		reservation, err := s.store.Reservation(ctx, r.ReservationID)
		if err != nil {
			return empty, err
		}
		if err = matchReservation(reservation, r, s.now()); err != nil {
			return empty, err
		}
		dnsCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		records, lookupErr := s.config.Resolver.LookupTXT(dnsCtx, "_hl-license."+r.Domain)
		cancel()
		found := false
		for _, value := range records {
			if value == reservation.DNSValue {
				found = true
			}
		}
		if lookupErr != nil || !found {
			return empty, ErrDNS
		}
	}
	raw, _, err := s.store.Run(ctx, opKey, digest, func(tx transaction) ([]byte, error) {
		var result any
		switch operation {
		case "prepare":
			challenge, err := s.prepare(ctx, tx, r)
			if err != nil {
				return nil, err
			}
			result = challenge
		case "commit":
			claims, err := s.commit(ctx, tx, r)
			if err != nil {
				return nil, err
			}
			result = claims
		case "sync":
			l, err := tx.Get(ctx, r.LicenseID)
			if err != nil {
				return nil, err
			}
			if err = normalizeStoredEntitlements(&l.Record); err != nil {
				return nil, err
			}
			if !matchBinding(l, r) {
				return nil, ErrBinding
			}
			result = s.claims(l, r.RequestID)
		}
		e, err := p.Sign(result, s.config.KeyID, s.config.SigningKey)
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
func matchBinding(l license, r p.Request) bool {
	return l.Record.Domain == r.Domain && l.Record.InstanceID == r.InstanceID && l.InstancePublicKey == r.InstancePublicKey
}
func matchReservation(v reservation, r p.Request, now time.Time) error {
	if !now.Before(v.ExpiresAt) {
		return ErrChallengeExpired
	}
	if v.Domain != r.Domain || v.InstanceID != r.InstanceID || v.PublicKey != r.InstancePublicKey {
		return ErrBinding
	}
	return nil
}
func (s *Service) prepare(ctx context.Context, tx transaction, r p.Request) (p.Challenge, error) {
	var empty p.Challenge
	l, err := tx.ByCode(ctx, hash(r.Code))
	if err != nil {
		return empty, err
	}
	if err = normalizeStoredEntitlements(&l.Record); err != nil {
		return empty, err
	}
	if l.Record.RevokedAt != nil {
		return empty, ErrRevoked
	}
	if l.Record.Domain != r.Domain {
		return empty, ErrBinding
	}
	if l.Record.InstanceID != "" && !matchBinding(l, r) {
		return empty, ErrBinding
	}
	now := s.now()
	reservation, exists, err := tx.FindReservation(ctx, l.Record.ID, r.InstanceID, r.InstancePublicKey, now)
	if err != nil {
		return empty, err
	}
	if !exists {
		id, err := randomID("res_")
		if err != nil {
			return empty, err
		}
		nonce, err := randomID("hl-license-v2=")
		if err != nil {
			return empty, err
		}
		reservation = structReservation(id, l, r, nonce, now)
		if err = tx.SaveReservation(ctx, reservation); err != nil {
			return empty, err
		}
		if err = s.audit(ctx, tx, l, "activation-prepare", p.Actor{ID: r.InstanceID, Name: "HL instance"}, "DNS ownership challenge prepared"); err != nil {
			return empty, err
		}
	}
	key, _ := base64.RawURLEncoding.DecodeString(r.InstancePublicKey)
	return p.Challenge{Version: p.Version, Issuer: s.config.Issuer, Product: p.Product, Audience: p.Audience, RequestID: r.RequestID, Domain: r.Domain, InstanceID: r.InstanceID, InstanceKeyThumbprint: p.Thumbprint(ed25519.PublicKey(key)), ReservationID: reservation.ID, DNSName: "_hl-license." + r.Domain, DNSValue: reservation.DNSValue, IssuedAt: now, ExpiresAt: reservation.ExpiresAt}, nil
}
func structReservation(id string, l license, r p.Request, nonce string, now time.Time) reservation {
	return reservation{ID: id, LicenseID: l.Record.ID, Domain: r.Domain, InstanceID: r.InstanceID, PublicKey: r.InstancePublicKey, CodeHash: l.CodeHash, DNSValue: nonce, CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute)}
}
func (s *Service) commit(ctx context.Context, tx transaction, r p.Request) (p.Claims, error) {
	var empty p.Claims
	reservation, err := tx.Reservation(ctx, r.ReservationID)
	if err != nil {
		return empty, err
	}
	l, err := tx.Get(ctx, reservation.LicenseID)
	if err != nil {
		return empty, err
	}
	if err = normalizeStoredEntitlements(&l.Record); err != nil {
		return empty, err
	}
	// Re-read after locking the license, since another transaction may have
	// invalidated the reservation while this transaction waited for its lock.
	reservation, err = tx.Reservation(ctx, r.ReservationID)
	if err != nil {
		return empty, err
	}
	if err = matchReservation(reservation, r, s.now()); err != nil {
		return empty, err
	}
	if l.Record.RevokedAt != nil {
		return empty, ErrRevoked
	}
	if l.Record.Domain != r.Domain || l.CodeHash != reservation.CodeHash {
		return empty, ErrBinding
	}
	if l.Record.InstanceID != "" && !matchBinding(l, r) {
		return empty, ErrBinding
	}
	if l.Record.InstanceID == "" {
		now := s.now()
		if l.Record.ActivatedAt == nil {
			l.Record.ActivatedAt = &now
			l.Record.LicenseExpiresAt, err = addTerm(now, l.Record.Term)
			if err != nil {
				return empty, err
			}
		}
		l.Record.InstanceID = r.InstanceID
		l.InstancePublicKey = r.InstancePublicKey
		l.Record.Revision++
		l.Record.Status = status(l.Record, now)
		if err = tx.Save(ctx, l); err != nil {
			return empty, err
		}
		if err = s.audit(ctx, tx, l, "activate", p.Actor{ID: r.InstanceID, Name: "HL instance"}, "DNS ownership and instance signature verified"); err != nil {
			return empty, err
		}
	}
	return s.claims(l, r.RequestID), nil
}
func (s *Service) claims(l license, requestID string) p.Claims {
	now := s.now()
	key, _ := base64.RawURLEncoding.DecodeString(l.InstancePublicKey)
	state := status(l.Record, now)
	lease := now.Add(24 * time.Hour)
	if state == "active" && l.Record.LicenseExpiresAt != nil && l.Record.LicenseExpiresAt.Before(lease) {
		lease = *l.Record.LicenseExpiresAt
	}
	refresh := now.Add(30 * time.Minute)
	if !refresh.Before(lease) {
		refresh = now.Add(lease.Sub(now) / 2)
	}
	return p.Claims{Version: p.Version, Issuer: s.config.Issuer, Product: p.Product, Audience: p.Audience, RequestID: requestID, LicenseID: l.Record.ID, Domain: l.Record.Domain, InstanceID: l.Record.InstanceID, InstanceKeyThumbprint: p.Thumbprint(ed25519.PublicKey(key)), Revision: l.Record.Revision, Status: state, Tier: l.Record.Tier, Limits: l.Record.Limits, Term: l.Record.Term, ActivatedAt: *l.Record.ActivatedAt, LicenseExpiresAt: l.Record.LicenseExpiresAt, IssuedAt: now, RefreshAfter: refresh, LeaseValidUntil: lease}
}
