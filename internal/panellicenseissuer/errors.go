// Package panellicenseissuer owns the standalone licensing authority. It never
// reads or writes the panel's customer database.
package panellicenseissuer

import "errors"

type Error struct {
	Code       string
	Message    string
	HTTPStatus int
}

func (e *Error) Error() string                       { return e.Message }
func problem(code, message string, status int) error { return &Error{code, message, status} }
func invalid(message string) error                   { return problem("invalid_request", message, 400) }

var (
	ErrNotFound            = problem("license_not_found", "license not found", 404)
	ErrDomainConflict      = problem("domain_conflict", "domain already belongs to another license", 409)
	ErrRevisionConflict    = problem("revision_conflict", "license changed; reload before retrying", 409)
	ErrIdempotencyConflict = problem("idempotency_conflict", "idempotency key used with different input", 409)
	ErrBinding             = problem("binding_mismatch", "license belongs to another domain or instance", 409)
	ErrRevoked             = problem("license_revoked", "revoked licenses cannot be extended or activated", 409)
	ErrInvalidCode         = problem("invalid_code", "invalid license code", 403)
	ErrChallengeExpired    = problem("challenge_expired", "activation challenge expired; prepare again", 409)
	ErrDNS                 = problem("dns_not_verified", "activation DNS TXT record not verified", 409)
)

func publicError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{"internal_error", "license authority request failed", 500}
}
