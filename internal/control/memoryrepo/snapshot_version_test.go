package memoryrepo

import (
	"encoding/json"
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

func TestDecodeCompatibleLiveSnapshotVersion(t *testing.T) {
	hash, err := auth.HashPassword("snapshot-compatible-test")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := New(auth.Administrator{ID: "admin", Username: "admin", PasswordHash: hash}).EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	fields["Version"] = json.RawMessage("21")
	compatible, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSnapshotAtVersion(compatible, 21); err != nil {
		t.Fatalf("compatible live snapshot rejected: %v", err)
	}
}
