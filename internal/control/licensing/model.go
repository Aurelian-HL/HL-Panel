package licensing

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
)

const (
	StatusActive  = "active"
	StatusRevoked = "revoked"
	StatusExpired = "expired"
)

var ErrInvalidCode = errors.New("license code is invalid")
var ErrDomainMismatch = errors.New("license domain does not match")
var ErrExpired = errors.New("license has expired")
var ErrRevoked = errors.New("license has been revoked")
var ErrBound = errors.New("license is bound to another instance")

var domainPattern = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

type Code struct {
	ID              string     `json:"id"`
	CodeHash        string     `json:"code_hash"`
	Prefix          string     `json:"prefix"`
	Domain          string     `json:"domain"`
	Months          int        `json:"months"`
	IssuedAt        time.Time  `json:"issued_at"`
	ActivatedAt     *time.Time `json:"activated_at,omitempty"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	Status          string     `json:"status"`
	BoundInstanceID string     `json:"bound_instance_id,omitempty"`
	LastRenewedAt   *time.Time `json:"last_renewed_at,omitempty"`
}

type CodeView struct {
	ID          string     `json:"id"`
	Prefix      string     `json:"prefix"`
	Domain      string     `json:"domain"`
	Months      int        `json:"months"`
	IssuedAt    time.Time  `json:"issued_at"`
	ActivatedAt *time.Time `json:"activated_at,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	Status      string     `json:"status"`
	Bound       bool       `json:"bound"`
}

type RevokeResult struct {
	ID        string    `json:"id"`
	RevokedAt time.Time `json:"revoked_at"`
}

type Lease struct {
	LicenseID   string    `json:"license_id"`
	InstanceID  string    `json:"instance_id"`
	Domain      string    `json:"domain"`
	ActivatedAt time.Time `json:"activated_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	CheckedAt   time.Time `json:"checked_at"`
}

type IssueInput struct {
	Domain string
	Months int
}

type Repository interface {
	IssueLicenseCode(ctx context.Context, item Code, event audit.Event, idempotencyKey, requestSHA256 string) (Code, bool, error)
	ListLicenseCodes(ctx context.Context, now time.Time) ([]Code, error)
	RevokeLicenseCode(ctx context.Context, input RevokeInput, event audit.Event) (RevokeResult, bool, error)
	ActivateLicenseCode(ctx context.Context, code, domain, instanceID string, now time.Time, event audit.Event) (Lease, error)
	RenewLicense(ctx context.Context, licenseID, instanceID, code, domain string, now time.Time, event audit.Event) (Lease, error)
	CurrentLicenseLease(ctx context.Context) (Lease, bool, error)
	StoreLicenseLease(ctx context.Context, lease Lease) error
}

type RevokeInput struct {
	ID             string
	RevokedBy      string
	IdempotencyKey string
	RequestSHA256  string
	RevokedAt      time.Time
}

func NormalizeDomain(value string) (string, error) {
	domain := strings.ToLower(strings.TrimSpace(value))
	if domain == "" || strings.ContainsAny(domain, "/?#@:\\ \t\r\n") {
		return "", ErrDomainMismatch
	}
	domain = strings.TrimSuffix(domain, ".")
	if ip := net.ParseIP(domain); ip != nil {
		// Licensing binds to a public IPv4 or a DNS name. IPv6 literals are
		// deliberately rejected so installer and runtime validation agree.
		if ip.To4() == nil {
			return "", ErrDomainMismatch
		}
		return ip.To4().String(), nil
	}
	if !domainPattern.MatchString(domain) {
		return "", ErrDomainMismatch
	}
	return domain, nil
}

func DomainMatches(expected, actual string) bool {
	a, errA := NormalizeDomain(expected)
	b, errB := NormalizeDomain(actual)
	return errA == nil && errB == nil && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func ValidateMonths(months int) error {
	if months < 1 || months > 120 {
		return errors.New("license months must be between 1 and 120")
	}
	return nil
}

const leaseClockSkew = 5 * time.Minute

// ValidateLease checks the shape of a persisted lease without applying an
// expiry decision. Expired leases must remain restorable so the caller can
// report the correct expired state instead of treating a damaged snapshot as
// an empty database.
func ValidateLease(lease Lease) (Lease, error) {
	if !validLeaseIdentifier(lease.LicenseID) || !validLeaseIdentifier(lease.InstanceID) {
		return Lease{}, ErrInvalidCode
	}
	domain, err := NormalizeDomain(lease.Domain)
	if err != nil {
		return Lease{}, err
	}
	if lease.ActivatedAt.IsZero() || lease.ExpiresAt.IsZero() || lease.CheckedAt.IsZero() {
		return Lease{}, ErrInvalidCode
	}
	lease.LicenseID = strings.TrimSpace(lease.LicenseID)
	lease.InstanceID = strings.TrimSpace(lease.InstanceID)
	lease.Domain = domain
	lease.ActivatedAt = lease.ActivatedAt.UTC()
	lease.ExpiresAt = lease.ExpiresAt.UTC()
	lease.CheckedAt = lease.CheckedAt.UTC()
	if !lease.ExpiresAt.After(lease.ActivatedAt) || lease.CheckedAt.Before(lease.ActivatedAt) {
		return Lease{}, ErrInvalidCode
	}
	return lease, nil
}

// ValidateLeaseAt applies the runtime clock checks used when a remote issuer
// response is accepted or a lease is stored locally.
func ValidateLeaseAt(lease Lease, now time.Time) (Lease, error) {
	lease, err := ValidateLease(lease)
	if err != nil {
		return Lease{}, err
	}
	now = now.UTC()
	if lease.ActivatedAt.After(now.Add(leaseClockSkew)) || lease.CheckedAt.After(now.Add(leaseClockSkew)) {
		return Lease{}, ErrInvalidCode
	}
	if !lease.ExpiresAt.After(now) {
		return Lease{}, ErrExpired
	}
	return lease, nil
}

// ValidateStoredCode verifies the confidential code record before it is
// restored from a snapshot. The raw code is never part of this structure.
func ValidateStoredCode(item Code) (Code, error) {
	if !validLeaseIdentifier(item.ID) || !validLeaseIdentifier(item.Prefix) || item.IssuedAt.IsZero() {
		return Code{}, ErrInvalidCode
	}
	if len(item.CodeHash) != 64 || strings.ToLower(item.CodeHash) != item.CodeHash {
		return Code{}, ErrInvalidCode
	}
	if _, err := hex.DecodeString(item.CodeHash); err != nil {
		return Code{}, ErrInvalidCode
	}
	if err := ValidateMonths(item.Months); err != nil {
		return Code{}, ErrInvalidCode
	}
	domain, err := NormalizeDomain(item.Domain)
	if err != nil {
		return Code{}, err
	}
	if item.Status != StatusActive && item.Status != StatusRevoked && item.Status != StatusExpired {
		return Code{}, ErrInvalidCode
	}
	item.ID = strings.TrimSpace(item.ID)
	item.Prefix = strings.TrimSpace(item.Prefix)
	item.Domain = domain
	item.IssuedAt = item.IssuedAt.UTC()
	if item.ActivatedAt != nil {
		activated := item.ActivatedAt.UTC()
		item.ActivatedAt = &activated
		if activated.Before(item.IssuedAt) || !validLeaseIdentifier(item.BoundInstanceID) {
			return Code{}, ErrInvalidCode
		}
	}
	if item.ExpiresAt != nil {
		expires := item.ExpiresAt.UTC()
		item.ExpiresAt = &expires
		if item.ActivatedAt == nil || !expires.After(*item.ActivatedAt) {
			return Code{}, ErrInvalidCode
		}
	}
	if item.LastRenewedAt != nil {
		renewed := item.LastRenewedAt.UTC()
		item.LastRenewedAt = &renewed
		if item.ActivatedAt == nil || renewed.Before(*item.ActivatedAt) {
			return Code{}, ErrInvalidCode
		}
	}
	if item.Status == StatusExpired && item.ExpiresAt == nil {
		return Code{}, ErrInvalidCode
	}
	if item.ActivatedAt == nil && item.BoundInstanceID != "" {
		return Code{}, ErrInvalidCode
	}
	return item, nil
}

func validLeaseIdentifier(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || len(trimmed) > 256 || trimmed != value {
		return false
	}
	for _, char := range trimmed {
		if char < 0x21 || char == 0x7f || char == '\u2028' || char == '\u2029' {
			return false
		}
	}
	return true
}

func (c Code) View(now time.Time) CodeView {
	status := c.Status
	if status == StatusActive && c.ExpiresAt != nil && !now.Before(*c.ExpiresAt) {
		status = StatusExpired
	}
	return CodeView{ID: c.ID, Prefix: c.Prefix, Domain: c.Domain, Months: c.Months, IssuedAt: c.IssuedAt, ActivatedAt: c.ActivatedAt, ExpiresAt: c.ExpiresAt, Status: status, Bound: c.BoundInstanceID != ""}
}
