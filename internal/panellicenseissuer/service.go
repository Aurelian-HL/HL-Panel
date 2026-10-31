package panellicenseissuer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	p "github.com/hongle/hl-panel/internal/panellicense"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

type TXTResolver interface {
	LookupTXT(context.Context, string) ([]string, error)
}
type Config struct {
	Issuer     string
	KeyID      string
	SigningKey ed25519.PrivateKey
	AdminToken string
	Clock      func() time.Time
	Resolver   TXTResolver
	// HTTPVerifier is an embedding/test seam, never an operator-configurable
	// network bypass. The command always uses the strict public HTTP-01 verifier.
	HTTPVerifier HTTPProofVerifier
	// Independent 32-byte AES key used only for recoverable inventory exports.
	ExportKey          []byte
	Products           []p.ShopProduct
	RedemptionVerifier RedemptionProofVerifier
}
type Service struct {
	store  repository
	config Config
}

func New(store *Postgres, config Config) (*Service, error) { return newService(store, config) }
func newService(store repository, config Config) (*Service, error) {
	u, err := url.Parse(config.Issuer)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("issuer must be an HTTPS identity URL")
	}
	if len(config.SigningKey) != ed25519.PrivateKeySize || len(config.AdminToken) < 32 || len(config.AdminToken) > 4096 || len(config.KeyID) == 0 || len(config.KeyID) > 128 || strings.ContainsAny(config.KeyID, " \t\r\n") {
		return nil, errors.New("issuer key and management token required")
	}
	for _, c := range config.AdminToken {
		if c < 33 || c > 126 {
			return nil, errors.New("invalid issuer management token")
		}
	}
	if !bytes.Equal(config.SigningKey, ed25519.NewKeyFromSeed(config.SigningKey.Seed())) {
		return nil, errors.New("invalid issuer signing key")
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	if config.Resolver == nil {
		config.Resolver = net.DefaultResolver
	}
	if config.HTTPVerifier == nil {
		config.HTTPVerifier = newHTTPProofVerifier()
	}
	if len(config.ExportKey) != 0 && len(config.ExportKey) != 32 {
		return nil, errors.New("inventory export key must contain 32 bytes")
	}
	config.ExportKey = append([]byte(nil), config.ExportKey...)
	if config.Products == nil {
		config.Products = p.DefaultShopProducts()
	}
	if err := validateProducts(config.Products); err != nil {
		return nil, err
	}
	config.Products = append([]p.ShopProduct(nil), config.Products...)
	if config.RedemptionVerifier == nil {
		config.RedemptionVerifier = newHTTPProofVerifier()
	}
	config.SigningKey = append(ed25519.PrivateKey(nil), config.SigningKey...)
	return &Service{store: store, config: config}, nil
}
func (s *Service) now() time.Time { return s.config.Clock().UTC() }
func randomID(prefix string) (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b[:]), nil
}
func hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func fingerprint(action string, value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return hash(action + "\n" + string(raw)), nil
}
func validateKey(key string) error {
	if len(key) < 16 || len(key) > 128 {
		return invalid("Idempotency-Key must contain 16 to 128 characters")
	}
	for _, c := range key {
		if c < 33 || c > 126 {
			return invalid("invalid Idempotency-Key")
		}
	}
	return nil
}
func validateActor(actor p.Actor, reason string) error {
	if len(actor.ID) < 1 || len(actor.ID) > 100 || !utf8.ValidString(actor.Name) || strings.TrimSpace(actor.Name) == "" || utf8.RuneCountInString(strings.TrimSpace(actor.Name)) > 100 {
		return invalid("trusted actor id and name required")
	}
	for _, c := range actor.ID {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_.:@-", c)) {
			return invalid("invalid trusted actor id")
		}
	}
	for _, c := range actor.Name {
		if c < 32 {
			return invalid("invalid trusted actor name")
		}
	}
	reasonLength := utf8.RuneCountInString(strings.TrimSpace(reason))
	if !utf8.ValidString(reason) || reasonLength < 2 || reasonLength > 500 {
		return invalid("reason must contain 2 to 500 characters")
	}
	for _, c := range reason {
		if c < 32 && c != '\n' && c != '\t' {
			return invalid("invalid reason characters")
		}
	}
	return nil
}
func (s *Service) audit(ctx context.Context, tx transaction, l license, action string, actor p.Actor, reason string) error {
	id, err := randomID("aud_")
	if err != nil {
		return err
	}
	return tx.Audit(ctx, p.AuditEvent{ID: id, LicenseID: l.Record.ID, Action: action, Actor: actor, Reason: reason, Revision: l.Record.Revision, At: s.now()})
}
func (s *Service) List(ctx context.Context) ([]p.LicenseRecord, error) {
	items, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	for i := range items {
		if err = normalizeStoredEntitlements(&items[i]); err != nil {
			return nil, err
		}
		items[i].Status = status(items[i], now)
	}
	return items, nil
}
func (s *Service) Audit(ctx context.Context, id string) ([]p.AuditEvent, error) {
	return s.store.Audit(ctx, id)
}
func (s *Service) Issue(ctx context.Context, key string, input p.IssueRequest) (p.MutationResult, error) {
	var result p.MutationResult
	if err := validateKey(key); err != nil {
		return result, err
	}
	if err := validateActor(input.Actor, input.Reason); err != nil {
		return result, err
	}
	domain, err := p.NormalizeDomain(input.Domain)
	if err != nil {
		return result, invalid("invalid license domain")
	}
	input.Domain = domain
	if input.Term.Validate() != nil {
		return result, invalid("invalid license term")
	}
	tier := input.Tier
	if tier == "" {
		tier = p.TierLicensed
	}
	if tier != p.TierLicensed {
		return result, invalid("free domains must register through DNS self-service")
	}
	limits, err := validateTierTerm(tier, input.Term)
	if err != nil {
		return result, err
	}
	digest, err := fingerprint("issue", input)
	if err != nil {
		return result, err
	}
	var code string
	raw, replayed, err := s.store.Run(ctx, "admin:"+key, digest, func(tx transaction) ([]byte, error) {
		if input.Term.Kind == "until" && !input.Term.ExpiresAt.After(s.now()) {
			return nil, invalid("expiry must be in the future")
		}
		id, err := randomID("lic_")
		if err != nil {
			return nil, err
		}
		if err = tx.ClaimDomain(ctx, domain, id); err != nil {
			return nil, err
		}
		code, err = randomID("hl2_")
		if err != nil {
			return nil, err
		}
		now := s.now()
		l := license{Record: p.LicenseRecord{ID: id, Domain: domain, Tier: tier, Limits: limits, Term: input.Term, Revision: 1, Status: "pending", CodePrefix: code[:10], CreatedAt: now}, CodeHash: hash(code)}
		if input.Term.Kind == "until" {
			l.Record.LicenseExpiresAt = input.Term.ExpiresAt
		}
		if err = tx.Save(ctx, l); err != nil {
			return nil, err
		}
		if err = s.audit(ctx, tx, l, "issue", input.Actor, input.Reason); err != nil {
			return nil, err
		}
		return json.Marshal(p.MutationResult{License: l.Record})
	})
	if err != nil {
		return result, err
	}
	if err = json.Unmarshal(raw, &result); err != nil {
		return result, err
	}
	if err = normalizeStoredEntitlements(&result.License); err != nil {
		return result, err
	}
	result.Replayed = replayed
	if !replayed {
		result.Code = code
	}
	return result, nil
}
func (s *Service) Mutate(ctx context.Context, id, action, key string, input p.MutationRequest) (p.MutationResult, error) {
	var result p.MutationResult
	if err := validateKey(key); err != nil {
		return result, err
	}
	if err := validateActor(input.Actor, input.Reason); err != nil {
		return result, err
	}
	if input.ExpectedRevision < 1 {
		return result, invalid("expected_revision required")
	}
	if action != "change-tier" && input.Tier != "" {
		return result, invalid("tier changes require change-tier")
	}
	switch action {
	case "change-tier":
		if input.Term == nil || input.Domain != "" {
			return result, invalid("change-tier requires tier and term only")
		}
		if _, err := validateTierTerm(input.Tier, *input.Term); err != nil {
			return result, err
		}
	case "extend":
		if input.Term == nil || input.Term.Validate() != nil || input.Domain != "" {
			return result, invalid("extend requires only a valid term")
		}
	case "rebind":
		if input.Term != nil {
			return result, invalid("rebind cannot change term")
		}
		if input.Domain != "" {
			d, err := p.NormalizeDomain(input.Domain)
			if err != nil {
				return result, invalid("invalid license domain")
			}
			input.Domain = d
		}
	case "revoke", "rotate-code":
		if input.Term != nil || input.Domain != "" {
			return result, invalid("unexpected term or domain")
		}
	default:
		return result, invalid("unknown license operation")
	}
	digest, err := fingerprint(id+"/"+action, input)
	if err != nil {
		return result, err
	}
	var code string
	raw, replayed, err := s.store.Run(ctx, "admin:"+key, digest, func(tx transaction) ([]byte, error) {
		l, err := tx.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if err = normalizeStoredEntitlements(&l.Record); err != nil {
			return nil, err
		}
		if l.Record.Revision != input.ExpectedRevision {
			return nil, ErrRevisionConflict
		}
		now := s.now()
		switch action {
		case "change-tier":
			if err = changeTier(&l.Record, input.Tier, *input.Term, now); err != nil {
				return nil, err
			}
		case "extend":
			if _, err = validateTierTerm(l.Record.Tier, *input.Term); err != nil {
				return nil, err
			}
			if err = extendTerm(&l.Record, *input.Term, now); err != nil {
				return nil, err
			}
		case "revoke":
			if l.Record.RevokedAt != nil {
				return nil, problem("operation_not_allowed", "license already revoked", 409)
			}
			l.Record.RevokedAt = &now
		case "rebind":
			if l.Record.RevokedAt != nil {
				return nil, ErrRevoked
			}
			if input.Domain != "" {
				if err = tx.ClaimDomain(ctx, input.Domain, id); err != nil {
					return nil, err
				}
				l.Record.Domain = input.Domain
			}
			l.Record.InstanceID = ""
			l.InstancePublicKey = ""
		}
		if action == "rotate-code" || action == "rebind" {
			code, err = randomID("hl2_")
			if err != nil {
				return nil, err
			}
			l.CodeHash = hash(code)
			l.Record.CodePrefix = code[:10]
		}
		if action != "extend" && action != "change-tier" {
			if err = tx.ClearReservations(ctx, id); err != nil {
				return nil, err
			}
		}
		l.Record.Revision++
		l.Record.Status = status(l.Record, now)
		if err = tx.Save(ctx, l); err != nil {
			return nil, err
		}
		if err = s.audit(ctx, tx, l, action, input.Actor, input.Reason); err != nil {
			return nil, err
		}
		return json.Marshal(p.MutationResult{License: l.Record})
	})
	if err != nil {
		return result, err
	}
	if err = json.Unmarshal(raw, &result); err != nil {
		return result, err
	}
	if err = normalizeStoredEntitlements(&result.License); err != nil {
		return result, err
	}
	result.Replayed = replayed
	if !replayed {
		result.Code = code
	}
	return result, nil
}
