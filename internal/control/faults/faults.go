package faults

import "errors"

var (
	ErrNotFound               = errors.New("not found")
	ErrUnauthorized           = errors.New("unauthorized")
	ErrPasswordChangeRequired = errors.New("password change required")
	ErrValidation             = errors.New("validation failed")
	ErrConflict               = errors.New("conflict")
	ErrIdempotencyConflict    = errors.New("idempotency key was already used with different content")
	ErrExpired                = errors.New("expired")
	ErrAlreadyUsed            = errors.New("already used")
)
