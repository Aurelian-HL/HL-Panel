package panellicenseissuer

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	p "github.com/hongle/hl-panel/internal/panellicense"
	"strings"
	"time"
)

type RedemptionProofVerifier interface {
	VerifyRedemptionProof(context.Context, string, string, string) ([]string, error)
}

func (s *Service) shopCatalog(r p.Request) (p.Envelope, error) {
	if r.Code != "" || r.Challenge != nil || r.ReservationID != "" || r.LicenseID != "" {
		return p.Envelope{}, invalid("catalog requires only instance identity")
	}
	return p.Sign(p.ShopCatalog{Purpose: "shop-catalog", Version: p.Version, Issuer: s.config.Issuer, Product: p.Product, Audience: p.Audience, RequestID: r.RequestID, IssuedAt: s.now(), ExpiresAt: s.now().Add(10 * time.Minute), Products: s.config.Products}, s.config.KeyID, s.config.SigningKey)
}
func redeemedBy(c stockCode, r p.Request) bool {
	return c.Redemption != nil && c.Redemption.Domain == r.Domain && c.Redemption.InstanceID == r.InstanceID && c.Redemption.InstancePublicKey == r.InstancePublicKey
}

func (s *Service) redemptionPublic(ctx context.Context, operation string, r p.Request, signed p.InstanceRequest) (p.Envelope, error) {
	if operation == "redeem-prepare" {
		if !strings.HasPrefix(r.Code, "hlr1_") || len(r.Code) != 37 || r.ReservationID != "" || r.LicenseID != "" || r.Challenge != nil {
			return p.Envelope{}, ErrInvalidCode
		}
		rawCode, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(r.Code, "hlr1_"))
		if err != nil || len(rawCode) != 24 || base64.RawURLEncoding.EncodeToString(rawCode) != strings.TrimPrefix(r.Code, "hlr1_") {
			return p.Envelope{}, ErrInvalidCode
		}
		digest, err := fingerprint(operation, signed)
		if err != nil {
			return p.Envelope{}, err
		}
		preview, err := s.store.StockCode(ctx, hash(r.Code))
		if err != nil {
			return p.Envelope{}, err
		}
		// The code itself is absent from the replay result; only its hash is signed.
		raw, _, err := s.store.Run(ctx, "redeem-prepare:"+r.RequestID, digest, func(tx transaction) ([]byte, error) {
			batch, err := tx.StockBatch(ctx, preview.BatchID, false)
			if err != nil {
				return nil, err
			}
			code, err := tx.StockCode(ctx, preview.Hash)
			if err != nil {
				return nil, err
			}
			if code.Redemption != nil && !redeemedBy(code, r) {
				return nil, problem("code_already_redeemed", "redemption code already used", 409)
			}
			if code.Redemption == nil && batch.Record.Disabled {
				return nil, problem("code_disabled", "redemption batch is disabled", 409)
			}
			id, err := randomID("redeem_")
			if err != nil {
				return nil, err
			}
			value, err := randomID("hl-license-v2=")
			if err != nil {
				return nil, err
			}
			key, _ := base64.RawURLEncoding.DecodeString(r.InstancePublicKey)
			a := p.RedemptionAttempt{Challenge: p.Challenge{Purpose: p.RedemptionPurpose, Version: p.Version, Issuer: s.config.Issuer, Product: p.Product, Audience: p.Audience, RequestID: r.RequestID, Domain: r.Domain, InstanceID: r.InstanceID, InstanceKeyThumbprint: p.Thumbprint(ed25519.PublicKey(key)), ReservationID: id, VerificationMethod: "https-01", HTTPPath: p.InstallProofPath + id, HTTPValue: value, IssuedAt: s.now(), ExpiresAt: s.now().Add(10 * time.Minute)}, CodeHash: code.Hash}
			envelope, err := p.Sign(a, s.config.KeyID, s.config.SigningKey)
			if err != nil {
				return nil, err
			}
			return json.Marshal(envelope)
		})
		if err != nil {
			return p.Envelope{}, err
		}
		var envelope p.Envelope
		err = json.Unmarshal(raw, &envelope)
		return envelope, err
	}
	if r.Challenge == nil || r.Code != "" || r.LicenseID != "" || r.ReservationID != "" {
		return p.Envelope{}, invalid("redemption commit requires only a signed receipt")
	}
	var attempt p.RedemptionAttempt
	keys := map[string]ed25519.PublicKey{s.config.KeyID: s.config.SigningKey.Public().(ed25519.PublicKey)}
	if err := p.Verify(*r.Challenge, keys, &attempt); err != nil {
		return p.Envelope{}, ErrBinding
	}
	key, _ := base64.RawURLEncoding.DecodeString(r.InstancePublicKey)
	if !p.ValidRedemptionProof(attempt) || attempt.Version != p.Version || attempt.Issuer != s.config.Issuer || attempt.Product != p.Product || attempt.Audience != p.Audience || attempt.Domain != r.Domain || attempt.InstanceID != r.InstanceID || attempt.InstanceKeyThumbprint != p.Thumbprint(ed25519.PublicKey(key)) || attempt.IssuedAt.After(s.now().Add(5*time.Minute)) || !attempt.ExpiresAt.After(attempt.IssuedAt) || attempt.ExpiresAt.Sub(attempt.IssuedAt) > 10*time.Minute {
		return p.Envelope{}, ErrBinding
	}
	preview, err := s.store.StockCode(ctx, attempt.CodeHash)
	if err != nil {
		return p.Envelope{}, err
	}
	var ips []string
	if preview.Redemption == nil {
		if !attempt.ExpiresAt.After(s.now()) {
			return p.Envelope{}, ErrChallengeExpired
		}
		// All DNS/TLS I/O happens outside database and forwarding locks.
		ips, err = s.config.RedemptionVerifier.VerifyRedemptionProof(ctx, r.Domain, attempt.HTTPPath, attempt.HTTPValue)
		if err != nil {
			return p.Envelope{}, problem("https_not_verified", "public HTTPS domain proof could not be verified", 409)
		}
	}
	var claims p.Claims
	err = s.store.Transact(ctx, func(tx transaction) error {
		batch, err := tx.StockBatch(ctx, preview.BatchID, false)
		if err != nil {
			return err
		}
		code, err := tx.StockCode(ctx, attempt.CodeHash)
		if err != nil {
			return err
		}
		if code.Redemption != nil {
			if !redeemedBy(code, r) {
				return problem("code_already_redeemed", "redemption code already used", 409)
			}
			l, err := tx.Get(ctx, code.Redemption.LicenseID)
			if err != nil {
				return err
			}
			if !matchBinding(l, r) {
				return ErrBinding
			}
			if err = normalizeStoredEntitlements(&l.Record); err != nil {
				return err
			}
			// Recovery always signs the current license, including revocation and
			// rebinding checks. A historical receipt never replays obsolete rights.
			claims = s.claims(l, r.RequestID)
			return nil
		}
		if batch.Record.Disabled {
			return problem("code_disabled", "redemption batch is disabled", 409)
		}
		if !attempt.ExpiresAt.After(s.now()) {
			return ErrChallengeExpired
		}
		l, err := tx.ByDomain(ctx, r.Domain)
		now := s.now()
		if errors.Is(err, ErrNotFound) {
			id, err := randomID("lic_")
			if err != nil {
				return err
			}
			internalCode, err := randomID("hl2_")
			if err != nil {
				return err
			}
			limits, _ := p.LimitsForTier(p.TierLicensed)
			l = license{Record: p.LicenseRecord{ID: id, Domain: r.Domain, Tier: p.TierLicensed, Limits: limits, Term: batch.Record.Term, CodePrefix: code.Prefix, CreatedAt: now, ActivatedAt: &now, InstanceID: r.InstanceID}, CodeHash: hash(internalCode), InstancePublicKey: r.InstancePublicKey}
			l.Record.LicenseExpiresAt, err = addTerm(now, batch.Record.Term)
			if err != nil {
				return err
			}
			if err = tx.ClaimDomain(ctx, r.Domain, id); err != nil {
				return err
			}
		} else {
			if err != nil {
				return err
			}
			if !matchBinding(l, r) {
				return ErrBinding
			}
			if err = normalizeStoredEntitlements(&l.Record); err != nil {
				return err
			}
			if l.Record.Tier == p.TierFree {
				err = changeTier(&l.Record, p.TierLicensed, batch.Record.Term, now)
			} else {
				err = extendTerm(&l.Record, batch.Record.Term, now)
			}
			if err != nil {
				return err
			}
		}
		l.Record.Revision++
		l.Record.Status = status(l.Record, now)
		if err = tx.Save(ctx, l); err != nil {
			return err
		}
		id, err := randomID("redemption_")
		if err != nil {
			return err
		}
		code.Redemption = &p.RedemptionRecord{ID: id, BatchID: batch.Record.ID, CodePrefix: code.Prefix, ProductID: batch.Record.ProductID, LicenseID: l.Record.ID, Domain: r.Domain, InstanceID: r.InstanceID, InstancePublicKey: r.InstancePublicKey, RedeemedAt: now, VerifiedIPs: ips, IPSource: "https-verified-dns"}
		if err = tx.SaveStockCode(ctx, code); err != nil {
			return err
		}
		actor := p.Actor{ID: r.InstanceID, Name: "HL instance"}
		reason := "Redeemed inventory batch " + batch.Record.ID
		if err = s.audit(ctx, tx, l, "redeem", actor, reason); err != nil {
			return err
		}
		if err = tx.StockAudit(ctx, batch.Record.ID, "redeem", p.StockActionRequest{Actor: actor, Reason: reason}, now); err != nil {
			return err
		}
		claims = s.claims(l, r.RequestID)
		return nil
	})
	if err != nil {
		return p.Envelope{}, err
	}
	return p.Sign(claims, s.config.KeyID, s.config.SigningKey)
}
