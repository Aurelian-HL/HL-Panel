// Package panellicense defines the isolated HL panel licensing v2 protocol.
package panellicense

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

const (
	Version        = 2
	Product        = "hl-panel"
	Audience       = "hl-panel-control"
	PublicPath     = "/api/v2/public/hl-license"
	ManagementPath = "/api/v2/license/codes"
)

type Term struct {
	Kind      string     `json:"kind"`
	Months    int        `json:"months,omitempty"`
	Days      int        `json:"days,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

func (t Term) Validate() error {
	switch t.Kind {
	case "calendar_months":
		if t.Months >= 1 && t.Months <= 120 && t.Days == 0 && t.ExpiresAt == nil {
			return nil
		}
	case "days":
		if t.Days >= 1 && t.Days <= 36500 && t.Months == 0 && t.ExpiresAt == nil {
			return nil
		}
	case "until":
		if t.ExpiresAt != nil && !t.ExpiresAt.IsZero() && t.Months == 0 && t.Days == 0 {
			return nil
		}
	case "perpetual":
		if t.Months == 0 && t.Days == 0 && t.ExpiresAt == nil {
			return nil
		}
	}
	return errors.New("invalid license term")
}

type Envelope struct {
	Payload   string `json:"payload"`
	KeyID     string `json:"key_id"`
	Signature string `json:"signature"`
}

// InstanceRequest signs the exact base64url JSON bytes, without JSON canonicalization.
type InstanceRequest struct {
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

type Request struct {
	Version           int       `json:"version"`
	Product           string    `json:"product"`
	Audience          string    `json:"audience"`
	RequestID         string    `json:"request_id"`
	IssuedAt          time.Time `json:"issued_at"`
	Domain            string    `json:"domain"`
	InstanceID        string    `json:"instance_id"`
	InstancePublicKey string    `json:"instance_public_key"`
	Code              string    `json:"code,omitempty"`
	ReservationID     string    `json:"reservation_id,omitempty"`
	LicenseID         string    `json:"license_id,omitempty"`
	Challenge         *Envelope `json:"challenge,omitempty"`
}

type Challenge struct {
	Purpose               string    `json:"purpose,omitempty"`
	Version               int       `json:"version"`
	Issuer                string    `json:"issuer"`
	Product               string    `json:"product"`
	Audience              string    `json:"audience"`
	RequestID             string    `json:"request_id"`
	Domain                string    `json:"domain"`
	InstanceID            string    `json:"instance_id"`
	InstanceKeyThumbprint string    `json:"instance_key_thumbprint"`
	ReservationID         string    `json:"reservation_id"`
	DNSName               string    `json:"dns_name"`
	DNSValue              string    `json:"dns_value"`
	VerificationMethod    string    `json:"verification_method,omitempty"`
	HTTPPath              string    `json:"http_path,omitempty"`
	HTTPValue             string    `json:"http_value,omitempty"`
	IssuedAt              time.Time `json:"issued_at"`
	ExpiresAt             time.Time `json:"expires_at"`
}

type Claims struct {
	Version               int        `json:"version"`
	Issuer                string     `json:"issuer"`
	Product               string     `json:"product"`
	Audience              string     `json:"audience"`
	RequestID             string     `json:"request_id"`
	LicenseID             string     `json:"license_id"`
	Domain                string     `json:"domain"`
	InstanceID            string     `json:"instance_id"`
	InstanceKeyThumbprint string     `json:"instance_key_thumbprint"`
	Revision              int64      `json:"revision"`
	Status                string     `json:"status"`
	Tier                  string     `json:"tier"`
	Limits                Limits     `json:"limits"`
	Term                  Term       `json:"term"`
	ActivatedAt           time.Time  `json:"activated_at"`
	LicenseExpiresAt      *time.Time `json:"license_expires_at"`
	IssuedAt              time.Time  `json:"issued_at"`
	RefreshAfter          time.Time  `json:"refresh_after"`
	LeaseValidUntil       time.Time  `json:"lease_valid_until"`
}

// Actor is populated by the trusted HT server, never taken from browser headers.
type Actor struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type IssueRequest struct {
	Domain string `json:"domain"`
	Tier   string `json:"tier,omitempty"`
	Term   Term   `json:"term"`
	Actor  Actor  `json:"actor"`
	Reason string `json:"reason"`
}
type MutationRequest struct {
	ExpectedRevision int64  `json:"expected_revision"`
	Actor            Actor  `json:"actor"`
	Reason           string `json:"reason"`
	Term             *Term  `json:"term,omitempty"`
	Domain           string `json:"domain,omitempty"`
	Tier             string `json:"tier,omitempty"`
}
type LicenseRecord struct {
	ID               string     `json:"id"`
	Domain           string     `json:"domain"`
	Tier             string     `json:"tier"`
	Limits           Limits     `json:"limits"`
	Term             Term       `json:"term"`
	Revision         int64      `json:"revision"`
	Status           string     `json:"status"`
	CodePrefix       string     `json:"code_prefix"`
	InstanceID       string     `json:"instance_id,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	ActivatedAt      *time.Time `json:"activated_at,omitempty"`
	LicenseExpiresAt *time.Time `json:"license_expires_at"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
}
type MutationResult struct {
	License  LicenseRecord `json:"license"`
	Code     string        `json:"code,omitempty"`
	Replayed bool          `json:"replayed"`
}
type AuditEvent struct {
	ID        string    `json:"id"`
	LicenseID string    `json:"license_id"`
	Action    string    `json:"action"`
	Actor     Actor     `json:"actor"`
	Reason    string    `json:"reason"`
	Revision  int64     `json:"revision"`
	At        time.Time `json:"at"`
}
type ErrorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func NormalizeDomain(value string) (string, error) {
	v := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
	if len(v) < 4 || len(v) > 253 || net.ParseIP(v) != nil || !strings.Contains(v, ".") {
		return "", errors.New("a DNS domain is required")
	}
	for _, label := range strings.Split(v, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("invalid DNS domain")
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", errors.New("use an ASCII DNS domain")
			}
		}
	}
	return v, nil
}

func Thumbprint(key ed25519.PublicKey) string {
	hash := sha256.Sum256(key)
	return hex.EncodeToString(hash[:])
}
func Encode(value any) (string, error) {
	raw, err := json.Marshal(value)
	return base64.RawURLEncoding.EncodeToString(raw), err
}
func Decode(payload string, target any) error {
	if len(payload) > 65536 {
		return errors.New("license payload too large")
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return errors.New("invalid license encoding")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("trailing license data")
	}
	return nil
}
func Sign(value any, keyID string, key ed25519.PrivateKey) (Envelope, error) {
	p, err := Encode(value)
	if err != nil {
		return Envelope{}, err
	}
	if len(key) != ed25519.PrivateKeySize || keyID == "" || strings.ContainsAny(keyID, "\r\n") {
		return Envelope{}, errors.New("invalid signing key")
	}
	s := ed25519.Sign(key, []byte("hl-panel-license-v2\n"+keyID+"\n"+p))
	return Envelope{Payload: p, KeyID: keyID, Signature: base64.RawURLEncoding.EncodeToString(s)}, nil
}
func Verify(envelope Envelope, keys map[string]ed25519.PublicKey, target any) error {
	key := keys[envelope.KeyID]
	sig, err := base64.RawURLEncoding.DecodeString(envelope.Signature)
	if err != nil || len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, []byte("hl-panel-license-v2\n"+envelope.KeyID+"\n"+envelope.Payload), sig) {
		return errors.New("untrusted license signature")
	}
	return Decode(envelope.Payload, target)
}
func SignRequest(operation string, request Request, key ed25519.PrivateKey) (InstanceRequest, error) {
	p, err := Encode(request)
	if err != nil {
		return InstanceRequest{}, err
	}
	if len(key) != ed25519.PrivateKeySize {
		return InstanceRequest{}, errors.New("invalid instance key")
	}
	return InstanceRequest{Payload: p, Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte("hl-panel-instance-v2\n"+operation+"\n"+p)))}, nil
}
func VerifyRequest(operation string, signed InstanceRequest, now time.Time) (Request, error) {
	var r Request
	if err := Decode(signed.Payload, &r); err != nil {
		return r, err
	}
	key, err := base64.RawURLEncoding.DecodeString(r.InstancePublicKey)
	sig, e := base64.RawURLEncoding.DecodeString(signed.Signature)
	if err != nil || e != nil || len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, []byte("hl-panel-instance-v2\n"+operation+"\n"+signed.Payload), sig) {
		return r, errors.New("invalid instance proof")
	}
	domain, err := NormalizeDomain(r.Domain)
	if err != nil || domain != r.Domain || r.Version != Version || r.Product != Product || r.Audience != Audience || len(r.InstanceID) < 8 || len(r.InstanceID) > 128 || len(r.RequestID) < 16 || len(r.RequestID) > 128 || strings.ContainsAny(r.InstanceID+r.RequestID, " \t\r\n") || r.IssuedAt.Before(now.Add(-5*time.Minute)) || r.IssuedAt.After(now.Add(5*time.Minute)) {
		return r, fmt.Errorf("invalid request identity or time")
	}
	return r, nil
}
