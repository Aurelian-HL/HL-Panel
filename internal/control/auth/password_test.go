package auth

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/hongle/hl-panel/internal/control/faults"
)

func TestPasswordHashRoundTripAndRandomSalt(t *testing.T) {
	first, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash first password: %v", err)
	}
	second, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash second password: %v", err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("password hashes must use independent random salts")
	}
	if !strings.HasPrefix(string(first), "$pbkdf2-sha256$v=1$i=600000$") {
		t.Fatalf("hash is not versioned as expected: %q", first)
	}
	if err := ValidatePasswordHash(first); err != nil {
		t.Fatalf("validate generated hash: %v", err)
	}
	if err := checkPassword(first, "correct horse battery staple"); err != nil {
		t.Fatalf("verify correct password: %v", err)
	}
	if err := checkPassword(first, "wrong"); !errors.Is(err, faults.ErrUnauthorized) {
		t.Fatalf("wrong password returned %v, want unauthorized", err)
	}
}

func TestPasswordHashRejectsMalformedAndExcessiveWorkFactors(t *testing.T) {
	tests := []string{
		"",
		"$bcrypt$not-supported",
		"$pbkdf2-sha256$v=2$i=600000$AAAA$AAAA",
		"$pbkdf2-sha256$v=1$i=99999$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"$pbkdf2-sha256$v=1$i=10000001$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"$pbkdf2-sha256$v=1$i=600000$not-base64!$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	}
	for _, encoded := range tests {
		t.Run(encoded, func(t *testing.T) {
			if err := ValidatePasswordHash([]byte(encoded)); !errors.Is(err, faults.ErrValidation) {
				t.Fatalf("ValidatePasswordHash(%q) = %v, want validation error", encoded, err)
			}
		})
	}
}

func TestHashPasswordRejectsEmptyPassword(t *testing.T) {
	if _, err := HashPassword(""); !errors.Is(err, faults.ErrValidation) {
		t.Fatalf("HashPassword(empty) = %v, want validation error", err)
	}
}
