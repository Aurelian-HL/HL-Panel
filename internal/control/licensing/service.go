package licensing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/idgen"
	"github.com/hongle/hl-panel/internal/securetoken"
)

type Service struct {
	repository Repository
	now        func() time.Time
}

func NewService(repository Repository, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repository: repository, now: now}
}

func (s *Service) Issue(ctx context.Context, actorID string, input IssueInput, key string) (Code, string, bool, error) {
	domain, err := NormalizeDomain(input.Domain)
	if err != nil {
		return Code{}, "", false, err
	}
	if err := ValidateMonths(input.Months); err != nil {
		return Code{}, "", false, err
	}
	key = strings.TrimSpace(key)
	if !validIdempotencyKey(key) {
		return Code{}, "", false, errors.New("idempotency key must contain 1 to 128 visible ASCII characters")
	}
	scopedKey := strings.TrimSpace(actorID) + "\x00license.issue\x00" + key
	requestSHA256 := issueRequestHash(domain, input.Months)
	id, err := idgen.New("lic")
	if err != nil {
		return Code{}, "", false, err
	}
	raw, err := securetoken.Generate("hlp")
	if err != nil {
		return Code{}, "", false, err
	}
	now := s.now().UTC()
	item := Code{ID: id, CodeHash: securetoken.Hash(raw), Prefix: prefix(raw), Domain: domain, Months: input.Months, IssuedAt: now, Status: StatusActive}
	event, err := audit.NewEvent(now, "administrator", actorID, "license.issue", "license_code", id, "succeeded", map[string]any{"domain": domain, "months": input.Months, "idempotency_key": key})
	if err != nil {
		return Code{}, "", false, err
	}
	saved, replayed, err := s.repository.IssueLicenseCode(ctx, item, event, scopedKey, requestSHA256)
	if err != nil {
		return Code{}, "", false, err
	}
	if replayed {
		return saved, "", true, nil
	}
	return saved, raw, false, nil
}

func issueRequestHash(domain string, months int) string {
	digest := sha256.Sum256([]byte(domain + "\x00" + strconv.Itoa(months)))
	return hex.EncodeToString(digest[:])
}

func validIdempotencyKey(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if char < 0x21 || char > 0x7e {
			return false
		}
	}
	return true
}

func (s *Service) List(ctx context.Context) ([]CodeView, error) {
	now := s.now().UTC()
	items, err := s.repository.ListLicenseCodes(ctx, now)
	if err != nil {
		return nil, err
	}
	views := make([]CodeView, 0, len(items))
	for _, item := range items {
		views = append(views, item.View(now))
	}
	return views, nil
}

func (s *Service) View(item Code) CodeView {
	return item.View(s.now().UTC())
}

func (s *Service) CurrentLease(ctx context.Context) (Lease, bool, error) {
	lease, ok, err := s.repository.CurrentLicenseLease(ctx)
	if err != nil || !ok {
		return lease, ok, err
	}
	if !s.now().UTC().Before(lease.ExpiresAt) {
		return lease, false, ErrExpired
	}
	return lease, true, nil
}

// StoreLease persists a lease obtained from a remote issuer. The raw license
// code is deliberately not part of a lease and therefore never enters the
// snapshot or API response.
func (s *Service) StoreLease(ctx context.Context, lease Lease) error {
	validated, err := ValidateLeaseAt(lease, s.now())
	if err != nil {
		return err
	}
	return s.repository.StoreLicenseLease(ctx, validated)
}

func (s *Service) Revoke(ctx context.Context, actorID, id, idempotencyKey string) (RevokeResult, bool, error) {
	actorID = strings.TrimSpace(actorID)
	id = strings.TrimSpace(id)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if actorID == "" || id == "" || !validIdempotencyKey(idempotencyKey) {
		return RevokeResult{}, false, fmt.Errorf("%w: license id and Idempotency-Key are required", faults.ErrValidation)
	}
	now := s.now().UTC()
	event, err := audit.NewEvent(now, "administrator", actorID, "license.revoke", "license_code", id, "succeeded", nil)
	if err != nil {
		return RevokeResult{}, false, err
	}
	return s.repository.RevokeLicenseCode(ctx, RevokeInput{ID: id, RevokedBy: actorID, IdempotencyKey: idempotencyKey, RequestSHA256: revokeRequestHash(id), RevokedAt: now}, event)
}

func revokeRequestHash(id string) string {
	digest := sha256.Sum256([]byte("license.revoke\x00" + id))
	return hex.EncodeToString(digest[:])
}

func (s *Service) Activate(ctx context.Context, code, domain, instanceID string) (Lease, error) {
	return s.activate(ctx, code, domain, instanceID, "license.activate")
}

func (s *Service) Renew(ctx context.Context, licenseID, instanceID, code, domain string) (Lease, error) {
	now := s.now().UTC()
	event, err := audit.NewEvent(now, "system", "license-client", "license.renew", "license_code", licenseID, "succeeded", map[string]any{"domain": strings.TrimSpace(domain)})
	if err != nil {
		return Lease{}, err
	}
	return s.repository.RenewLicense(ctx, licenseID, instanceID, code, domain, now, event)
}

func (s *Service) activate(ctx context.Context, code, domain, instanceID, action string) (Lease, error) {
	if strings.TrimSpace(code) == "" || strings.TrimSpace(instanceID) == "" {
		return Lease{}, ErrInvalidCode
	}
	normalized, err := NormalizeDomain(domain)
	if err != nil {
		return Lease{}, err
	}
	now := s.now().UTC()
	event, err := audit.NewEvent(now, "system", "license-client", action, "license_code", "", "succeeded", map[string]any{"domain": normalized})
	if err != nil {
		return Lease{}, err
	}
	return s.repository.ActivateLicenseCode(ctx, strings.TrimSpace(code), normalized, strings.TrimSpace(instanceID), now, event)
}

func prefix(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 12 {
		return value[:12]
	}
	return value
}
