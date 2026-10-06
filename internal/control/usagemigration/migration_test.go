package usagemigration

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEmbeddedMigrationsMatchCanonicalFilesAndFixedDigests(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate migration test file")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "../../../"))
	tests := []struct {
		name     string
		path     string
		embedded []byte
		digest   string
	}{
		{name: "up", path: filepath.Join(root, "migrations/000003_usage_ledger.up.sql"), embedded: embeddedUp, digest: UpSHA256},
		{name: "down", path: filepath.Join(root, "migrations/000003_usage_ledger.down.sql"), embedded: embeddedDown, digest: DownSHA256},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			canonical, err := os.ReadFile(test.path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(canonical, test.embedded) {
				t.Fatal("embedded migration differs from canonical migration")
			}
			actual := fmt.Sprintf("%x", sha256.Sum256(canonical))
			if actual != test.digest {
				t.Fatalf("fixed digest = %s, canonical digest = %s", test.digest, actual)
			}
		})
	}
}

func TestPrepareMigrationChecksDigestAndRemovesOnlyOuterTransaction(t *testing.T) {
	raw := []byte("BEGIN;\nSELECT 'BEGIN and COMMIT stay in the body';\nCOMMIT;\n")
	digest := fmt.Sprintf("%x", sha256.Sum256(raw))
	body, err := prepareMigration(raw, digest)
	if err != nil {
		t.Fatal(err)
	}
	if body != "SELECT 'BEGIN and COMMIT stay in the body';" {
		t.Fatalf("unexpected executable body: %q", body)
	}
	if _, err := prepareMigration(raw, strings.Repeat("0", 64)); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("checksum mismatch error = %v", err)
	}
}

func TestPrepareMigrationRequiresExplicitNonemptyTransaction(t *testing.T) {
	tests := [][]byte{
		[]byte("SELECT 1;\n"),
		[]byte("BEGIN;\nCOMMIT;\n"),
		[]byte("BEGIN;\nSELECT 1;\n"),
	}
	for _, raw := range tests {
		digest := fmt.Sprintf("%x", sha256.Sum256(raw))
		if _, err := prepareMigration(raw, digest); err == nil {
			t.Fatalf("invalid migration accepted: %q", raw)
		}
	}
}

func TestValidateStateRejectsVersionThreeChecksumDrift(t *testing.T) {
	valid := stateRecord{Name: Name, UpSHA256: UpSHA256, DownSHA256: DownSHA256}
	if err := validateState(valid); err != nil {
		t.Fatal(err)
	}
	for _, changed := range []stateRecord{
		{Name: "other", UpSHA256: UpSHA256, DownSHA256: DownSHA256},
		{Name: Name, UpSHA256: "different", DownSHA256: DownSHA256},
		{Name: Name, UpSHA256: UpSHA256, DownSHA256: "different"},
	} {
		if err := validateState(changed); !errors.Is(err, ErrChecksumMismatch) {
			t.Fatalf("state drift error = %v", err)
		}
	}
}
