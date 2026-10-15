package engine

import (
	"strings"
	"testing"
)

func TestEngineVersionParsingDoesNotExposeDiagnostics(t *testing.T) {
	cases := []struct{ family, output, want string }{
		{"Xray", "Xray 26.3.27 (Xray, Penetrates Everything.)\nA unified platform.\n", "26.3.27"},
		{"Xray", "Xray 26.9.25 (go1.26 linux/amd64)", "26.9.25"},
		{"gost", "gost 3.3.1 (go1.26 linux/amd64)\n", "3.3.1"},
		{"gost", "gost v3.3.1-nightly.20260922 (go1.26 linux/amd64)", "3.3.1-nightly.20260922"},
		{"Xray", "error: token-is-secret", "unknown"},
		{"Xray", "Xray secret-token", "unknown"},
		{"Xray", "gost 3.3.1", "unknown"},
		{"Xray", "Xray 26.3.27?secret=value", "unknown"},
		{"Xray", "", "unknown"},
		{"Xray", "Xray 26.3.27\n" + strings.Repeat("x", maxEngineVersionBytes), "unknown"},
	}
	for _, tc := range cases {
		if got := parseEngineVersion(tc.family, []byte(tc.output)); got != tc.want {
			t.Errorf("family=%s: version=%q, want %q", tc.family, got, tc.want)
		}
	}
}

func TestEngineVersionOutputLimitAndUnknownRunner(t *testing.T) {
	b := &engineVersionOutput{}
	if n, err := b.Write(make([]byte, maxEngineVersionBytes)); n != maxEngineVersionBytes || err != nil {
		t.Fatal("allowed output rejected")
	}
	if _, err := b.Write([]byte("x")); err == nil || b.Len() != maxEngineVersionBytes {
		t.Fatal("output limit not enforced")
	}
	xray := &XrayProcessAdapter{runner: &recordingXrayRunner{}}
	if xray.EngineVersion() != "unknown" {
		t.Fatal("test runner reported a guessed version")
	}
	if (&ExecXrayRunner{}).EngineVersion() != "unknown" || (&ExecGOSTRunner{}).EngineVersion() != "unknown" {
		t.Fatal("uninitialized runner reported a guessed version")
	}
}
