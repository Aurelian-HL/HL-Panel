package licensing

import (
	"errors"
	"testing"
)

func TestNormalizeDomainAcceptsDomainAndIPv4Only(t *testing.T) {
	if got, err := NormalizeDomain("Panel.Example.com."); err != nil || got != "panel.example.com" {
		t.Fatalf("domain normalization = %q, %v", got, err)
	}
	if got, err := NormalizeDomain("198.51.100.7"); err != nil || got != "198.51.100.7" {
		t.Fatalf("IPv4 normalization = %q, %v", got, err)
	}
	if _, err := NormalizeDomain("2001:db8::7"); !errors.Is(err, ErrDomainMismatch) {
		t.Fatalf("IPv6 was accepted: %v", err)
	}
}
