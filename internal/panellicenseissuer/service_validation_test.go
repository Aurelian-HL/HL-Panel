package panellicenseissuer

import (
	"crypto/ed25519"
	"crypto/rand"
	p "github.com/hongle/hl-panel/internal/panellicense"
	"strings"
	"testing"
)

func TestTrustedActorUnicodeBoundaries(t *testing.T) {
	validActor := p.Actor{ID: "ht-admin_1@example.test:super", Name: strings.Repeat("管", 100)}
	validReason := strings.Repeat("审", 500)
	if err := validateActor(validActor, validReason); err != nil {
		t.Fatalf("HT Unicode limits must be accepted: %v", err)
	}
	for _, tc := range []struct {
		name   string
		actor  p.Actor
		reason string
	}{
		{"long name", p.Actor{ID: "admin", Name: strings.Repeat("管", 101)}, validReason},
		{"long reason", validActor, strings.Repeat("审", 501)},
		{"short trimmed reason", validActor, " 审 "},
		{"blank name", p.Actor{ID: "admin", Name: "   "}, validReason},
		{"empty id", p.Actor{Name: "管理员"}, validReason},
		{"long id", p.Actor{ID: strings.Repeat("a", 101), Name: "管理员"}, validReason},
		{"non ASCII id", p.Actor{ID: "管理员", Name: "管理员"}, validReason},
		{"slash id", p.Actor{ID: "admin/name", Name: "管理员"}, validReason},
		{"name control", p.Actor{ID: "admin", Name: "管理员\t"}, validReason},
		{"reason control", validActor, "审核\r原因"},
		{"invalid UTF8 name", p.Actor{ID: "admin", Name: string([]byte{0xff, 0xfe})}, validReason},
		{"invalid UTF8 reason", validActor, string([]byte{0xff, 0xfe})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateActor(tc.actor, tc.reason); err == nil {
				t.Fatal("invalid audit identity or reason accepted")
			}
		})
	}
	if err := validateActor(p.Actor{ID: "admin", Name: "管理员"}, " 审核\n详细\t原因 "); err != nil {
		t.Fatalf("multiline reason rejected: %v", err)
	}
}

func TestIssuerRejectsInconsistentSigningKey(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	config := Config{Issuer: "https://issuer.example.test", KeyID: "test-key", SigningKey: key, AdminToken: strings.Repeat("t", 32)}
	if _, err := newService(nil, config); err != nil {
		t.Fatalf("valid issuer key rejected: %v", err)
	}
	config.SigningKey = append(ed25519.PrivateKey(nil), key...)
	config.SigningKey[ed25519.SeedSize] ^= 1
	if _, err := newService(nil, config); err == nil {
		t.Fatal("inconsistent seed and public key accepted")
	}
}

func TestManagementTokenMatchesHTTransport(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	config := Config{Issuer: "https://issuer.example.test", KeyID: "test-key", SigningKey: key}
	for _, token := range []string{strings.Repeat("t", 32), strings.Repeat("t", 4096)} {
		config.AdminToken = token
		if _, err := newService(nil, config); err != nil {
			t.Fatalf("valid HT-compatible token rejected: %v", err)
		}
	}
	for _, token := range []string{strings.Repeat("t", 31), strings.Repeat("t", 4097), strings.Repeat("t", 31) + " ", strings.Repeat("t", 31) + "\t", strings.Repeat("管", 32), strings.Repeat("t", 31) + string(rune(127))} {
		config.AdminToken = token
		if _, err := newService(nil, config); err == nil {
			t.Fatal("token incompatible with HT transport accepted")
		}
	}
}
