package serviceaddress

import "testing"

func TestCanonicalHostAndRejectMultipleEndpointOrCredentialInput(t *testing.T) {
	for input, expected := range map[string]string{" Example.TEST. ": "example.test", "2001:0db8::1": "2001:db8::1", "127.0.0.1": "127.0.0.1"} {
		actual, err := NormalizeHost(input)
		if err != nil || actual != expected {
			t.Fatalf("host normalization failed for %q", input)
		}
	}
	for _, input := range []string{"vless://identity@host", "user:password@host", "first,second", "first\nsecond", "host:443", "host/path", "-host", "host_", "fe80::1%ethernet", "."} {
		if host, err := NormalizeHost(input); err == nil || host != "" {
			t.Fatal("invalid endpoint was accepted")
		}
	}
}
