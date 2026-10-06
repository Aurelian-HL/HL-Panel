package engine

import "testing"

func TestSupportedGOSTVersions(t *testing.T) {
	for _, output := range []string{
		"gost 3.3.1 (go1.27.1 windows/amd64)\n",
		"gost v3.3.1-nightly.20260922 (go1.26.8 linux/amd64)\n",
	} {
		if !IsSupportedGOSTVersion(output) {
			t.Fatalf("verified version rejected: %q", output)
		}
	}
	for _, output := range []string{
		"", "gost 3.3.10 (go1.27.1 linux/amd64)",
		"gost v3.3.1-nightly.20260923 (go1.26.8 linux/amd64)",
		"gost v3.4.0 (go1.26.8 linux/amd64)",
	} {
		if IsSupportedGOSTVersion(output) {
			t.Fatalf("unverified version accepted: %q", output)
		}
	}
}
