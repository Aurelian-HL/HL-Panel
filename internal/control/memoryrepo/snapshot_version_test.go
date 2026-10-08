package memoryrepo

import (
	"testing"

	"github.com/hongle/hl-panel/internal/control/auth"
)

func TestDecodeSnapshotChecksEnvelopeVersionInOnePass(t *testing.T) {
	hash, err := auth.HashPassword("snapshot-version-test")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := New(auth.Administrator{ID: "admin", Username: "admin", PasswordHash: hash}).EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeSnapshotAtVersion(raw, SnapshotVersion); err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeSnapshotAtVersion(raw, SnapshotVersion-1); err == nil {
		t.Fatal("mismatched envelope version accepted")
	}
	if _, err = DecodeSnapshotAtVersion(append(raw, []byte(`{}`)...), SnapshotVersion); err == nil {
		t.Fatal("trailing snapshot data accepted")
	}
}
