package engine

import "testing"

func TestSupportedGOSTVersions(t *testing.T) {
	for _, output := range []string{
		"gost 3.3.1 (go1.27.1 windows/amd64)\n",
		"gost v3.3.1-nightly.20260922 (go1.26.8 linux/amd64)\n",
		"gost 3.3.1-nightly.20260922-hl-udp.1 (go1.27.0 linux/amd64)\n",
	} {
		if !IsSupportedGOSTVersion(output) {
			t.Fatalf("verified version rejected: %q", output)
		}
	}
	for _, output := range []string{
		"", "gost 3.3.10 (go1.27.1 linux/amd64)",
		"gost v3.3.1-nightly.20260923 (go1.26.8 linux/amd64)",
		"gost v3.4.0 (go1.26.8 linux/amd64)",
		"gost 3.3.1-nightly.20260922-hl-udp.2 (go1.27.0 linux/amd64)",
		"gost 3.3.1-nightly.20260922-hl-udp.1-extra (go1.27.0 linux/amd64)",
		"gost v3.3.1-nightly.20260922-hl-udp.1 (go1.27.0 linux/amd64)",
		"gost 3.3.1-nightly.20260922-hl-udp.1 (go1.26.8 linux/amd64)",
		"gost 3.3.1-nightly.20260922-hl-udp.1 (go1.27.0 linux/arm64)",
		"gost 3.3.1-nightly.20260922-hl-udp.1 (go1.27.0 windows/amd64)",
		"gost 3.3.1-nightly.20260922-hl-udp.1 (go1.27.0 linux/amd64) extra",
	} {
		if IsSupportedGOSTVersion(output) {
			t.Fatalf("unverified version accepted: %q", output)
		}
	}
}
