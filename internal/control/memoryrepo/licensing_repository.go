package memoryrepo

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/licensing"
	"github.com/hongle/hl-panel/internal/securetoken"
)

func (s *Store) IssueLicenseCode(_ context.Context, item licensing.Code, event audit.Event, idempotencyKey, requestSHA256 string) (licensing.Code, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var replay licensing.Code
	if replayed, err := s.replayBusinessLocked(idempotencyKey, requestSHA256, &replay); replayed || err != nil {
		return replay, replayed, err
	}
	validated, err := licensing.ValidateStoredCode(item)
	if err != nil {
		return licensing.Code{}, false, errors.New("invalid license code")
	}
	item = validated
	if _, exists := s.licenseCodes[item.ID]; exists {
		return licensing.Code{}, false, errors.New("license code already exists")
	}
	s.licenseCodes[item.ID] = item
	if err := s.recordBusinessLocked(idempotencyKey, requestSHA256, item.ID, item); err != nil {
		delete(s.licenseCodes, item.ID)
		return licensing.Code{}, false, err
	}
	s.appendAuditLocked(event)
	return item, false, nil
}

func (s *Store) ListLicenseCodes(_ context.Context, now time.Time) ([]licensing.Code, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]licensing.Code, 0, len(s.licenseCodes))
	for id, item := range s.licenseCodes {
		if item.Status == licensing.StatusActive && item.ExpiresAt != nil && !now.Before(*item.ExpiresAt) {
			item.Status = licensing.StatusExpired
			s.licenseCodes[id] = item
		}
		item.CodeHash = ""
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].IssuedAt.Equal(items[j].IssuedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].IssuedAt.After(items[j].IssuedAt)
	})
	return items, nil
}

func (s *Store) RevokeLicenseCode(_ context.Context, input licensing.RevokeInput, event audit.Event) (licensing.RevokeResult, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := input.RevokedBy + "\x00license.revoke\x00" + input.ID + "\x00" + input.IdempotencyKey
	var replay licensing.RevokeResult
	if replayed, err := s.replayBusinessLocked(key, input.RequestSHA256, &replay); replayed || err != nil {
		return replay, replayed, err
	}
	if input.ID == "" || input.RevokedBy == "" || !validLicenseIdempotencyKey(input.IdempotencyKey) || input.RequestSHA256 == "" || input.RevokedAt.IsZero() {
		return licensing.RevokeResult{}, false, errors.New("invalid license revoke mutation")
	}
	item, ok := s.licenseCodes[strings.TrimSpace(input.ID)]
	if !ok {
		return licensing.RevokeResult{}, false, licensing.ErrInvalidCode
	}
	if item.Status == licensing.StatusExpired {
		return licensing.RevokeResult{}, false, licensing.ErrExpired
	}
	itemBeforeRevoke := item
	item.Status = licensing.StatusRevoked
	s.licenseCodes[item.ID] = item
	result := licensing.RevokeResult{ID: item.ID, RevokedAt: input.RevokedAt.UTC()}
	if err := s.recordBusinessLocked(key, input.RequestSHA256, item.ID, result); err != nil {
		s.licenseCodes[item.ID] = itemBeforeRevoke
		return licensing.RevokeResult{}, false, err
	}
	s.appendAuditLocked(event)
	return result, false, nil
}

func validLicenseIdempotencyKey(value string) bool {
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

func (s *Store) ActivateLicenseCode(_ context.Context, rawCode, domain, instanceID string, now time.Time, event audit.Event) (licensing.Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hash := securetoken.Hash(strings.TrimSpace(rawCode))
	for id, item := range s.licenseCodes {
		if item.CodeHash != hash {
			continue
		}
		if item.Status == licensing.StatusRevoked {
			return licensing.Lease{}, licensing.ErrRevoked
		}
		if item.ExpiresAt != nil && !now.Before(*item.ExpiresAt) {
			item.Status = licensing.StatusExpired
			s.licenseCodes[id] = item
			return licensing.Lease{}, licensing.ErrExpired
		}
		if !licensing.DomainMatches(item.Domain, domain) {
			return licensing.Lease{}, licensing.ErrDomainMismatch
		}
		if item.BoundInstanceID != "" && item.BoundInstanceID != instanceID {
			return licensing.Lease{}, licensing.ErrBound
		}
		if item.BoundInstanceID == "" {
			item.BoundInstanceID = instanceID
			item.ActivatedAt = timePtr(now)
			expires := now.AddDate(0, item.Months, 0)
			item.ExpiresAt = timePtr(expires)
		}
		item.LastRenewedAt = timePtr(now)
		item.Status = licensing.StatusActive
		s.licenseCodes[id] = item
		lease := licensing.Lease{LicenseID: item.ID, InstanceID: instanceID, Domain: item.Domain, ActivatedAt: derefTime(item.ActivatedAt), ExpiresAt: derefTime(item.ExpiresAt), CheckedAt: now}
		s.appendAuditLocked(eventWithResource(event, item.ID))
		return lease, nil
	}
	return licensing.Lease{}, licensing.ErrInvalidCode
}

func (s *Store) RenewLicense(_ context.Context, licenseID, instanceID, rawCode, domain string, now time.Time, event audit.Event) (licensing.Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.licenseCodes[strings.TrimSpace(licenseID)]
	if !ok || item.CodeHash != securetoken.Hash(strings.TrimSpace(rawCode)) {
		return licensing.Lease{}, licensing.ErrInvalidCode
	}
	if item.Status == licensing.StatusRevoked {
		return licensing.Lease{}, licensing.ErrRevoked
	}
	if item.BoundInstanceID != instanceID {
		return licensing.Lease{}, licensing.ErrBound
	}
	if !licensing.DomainMatches(item.Domain, domain) {
		return licensing.Lease{}, licensing.ErrDomainMismatch
	}
	if item.ExpiresAt == nil || !now.Before(*item.ExpiresAt) {
		item.Status = licensing.StatusExpired
		s.licenseCodes[item.ID] = item
		return licensing.Lease{}, licensing.ErrExpired
	}
	item.LastRenewedAt = timePtr(now)
	s.licenseCodes[item.ID] = item
	lease := licensing.Lease{LicenseID: item.ID, InstanceID: instanceID, Domain: item.Domain, ActivatedAt: derefTime(item.ActivatedAt), ExpiresAt: derefTime(item.ExpiresAt), CheckedAt: now}
	s.appendAuditLocked(eventWithResource(event, item.ID))
	return lease, nil
}

func (s *Store) CurrentLicenseLease(_ context.Context) (licensing.Lease, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.licenseLease == nil {
		return licensing.Lease{}, false, nil
	}
	return *s.licenseLease, true, nil
}

func (s *Store) StoreLicenseLease(_ context.Context, lease licensing.Lease) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	validated, err := licensing.ValidateLease(lease)
	if err != nil {
		return err
	}
	copy := validated
	s.licenseLease = &copy
	return nil
}

func eventWithResource(event audit.Event, resourceID string) audit.Event {
	event.ResourceID = resourceID
	return event
}
func timePtr(value time.Time) *time.Time { copy := value; return &copy }
func derefTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}
