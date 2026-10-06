package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/hongle/hl-panel/internal/control/faults"
)

const (
	passwordHashAlgorithm  = "pbkdf2-sha256"
	passwordHashIterations = 600_000
	passwordHashBytes      = 32
	passwordSaltBytes      = 16
)

func HashPassword(password string) ([]byte, error) {
	if password == "" {
		return nil, fmt.Errorf("%w: password is required", faults.ErrValidation)
	}
	salt := make([]byte, passwordSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generate password salt: %w", err)
	}
	digest, err := pbkdf2.Key(sha256.New, password, salt, passwordHashIterations, passwordHashBytes)
	if err != nil {
		return nil, fmt.Errorf("derive password hash: %w", err)
	}
	encoded := fmt.Sprintf("$%s$v=1$i=%d$%s$%s",
		passwordHashAlgorithm,
		passwordHashIterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(digest),
	)
	return []byte(encoded), nil
}

func ValidatePasswordHash(hash []byte) error {
	_, _, _, err := parsePasswordHash(hash)
	return err
}

func checkPassword(hash []byte, password string) error {
	iterations, salt, expected, err := parsePasswordHash(hash)
	if err != nil {
		return faults.ErrUnauthorized
	}
	actual, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(expected))
	if err != nil || subtle.ConstantTimeCompare(actual, expected) != 1 {
		return faults.ErrUnauthorized
	}
	return nil
}

func parsePasswordHash(encoded []byte) (int, []byte, []byte, error) {
	parts := strings.Split(string(encoded), "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != passwordHashAlgorithm || parts[2] != "v=1" || !strings.HasPrefix(parts[3], "i=") {
		return 0, nil, nil, fmt.Errorf("%w: unsupported password hash format", faults.ErrValidation)
	}
	iterations, err := strconv.Atoi(strings.TrimPrefix(parts[3], "i="))
	if err != nil || iterations < 100_000 || iterations > 10_000_000 {
		return 0, nil, nil, fmt.Errorf("%w: invalid password hash iteration count", faults.ErrValidation)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < passwordSaltBytes {
		return 0, nil, nil, fmt.Errorf("%w: invalid password hash salt", faults.ErrValidation)
	}
	digest, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(digest) != passwordHashBytes {
		return 0, nil, nil, fmt.Errorf("%w: invalid password hash digest", faults.ErrValidation)
	}
	return iterations, salt, digest, nil
}
