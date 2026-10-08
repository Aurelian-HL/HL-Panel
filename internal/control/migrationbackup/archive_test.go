package migrationbackup

import (
	"bytes"
	"encoding/json"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/panelruntime"
	"testing"
	"time"
)

func fixture(t *testing.T) Bundle {
	t.Helper()
	hash, err := auth.HashPassword("isolated-password")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := memoryrepo.New(auth.Administrator{ID: "admin-one", Username: "admin", PasswordHash: hash, CreatedAt: time.Now().UTC()}).EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	usage := map[string]json.RawMessage{}
	for _, table := range Tables() {
		usage[table] = json.RawMessage("[]")
	}
	return Bundle{Manifest: Manifest{Format: 1, Version: "v0.1.47", CreatedAt: time.Now().UTC(), SourceURL: "https://panel.example.com"}, State: State{Snapshot: raw, Usage: usage}, Runtime: RuntimeSecrets{PasswordFingerprintKey: bytes.Repeat([]byte{9}, 32)}}
}
func TestArchiveEncryptionIntegrityAndCompatibility(t *testing.T) {
	b := fixture(t)
	raw, err := Seal(b, "isolated-backup-pass")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("Administrators")) {
		t.Fatal("unencrypted snapshot")
	}
	restored, err := Open(raw, "isolated-backup-pass", "v0.1.47")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored.State.Snapshot, b.State.Snapshot) {
		t.Fatal("snapshot changed")
	}
	if Describe(restored, Digest(raw)).Counts["administrators"] != 1 {
		t.Fatal("incorrect preview")
	}
	if _, err = Open(raw, "wrong-password", "v0.1.47"); err == nil {
		t.Fatal("wrong password accepted")
	}
	corrupt := append([]byte{}, raw...)
	corrupt[len(corrupt)-1] ^= 1
	if _, err = Open(corrupt, "isolated-backup-pass", "v0.1.47"); err == nil {
		t.Fatal("tampering accepted")
	}
	if _, err = Open(raw, "isolated-backup-pass", "v0.1.46"); err == nil {
		t.Fatal("newer archive accepted")
	}
	b.State.Usage[Tables()[0]] = json.RawMessage(`[{"unexpected":1}]`)
	if _, err = Seal(b, "isolated-backup-pass"); err == nil {
		t.Fatal("incompatible schema accepted")
	}
	b = fixture(t)
	b.Runtime.Logs = make([]panelruntime.LogEntry, 2000)
	for i := range b.Runtime.Logs {
		b.Runtime.Logs[i] = panelruntime.LogEntry{Time: time.Now().Format(time.RFC3339Nano), Level: "INFO", Message: string(bytes.Repeat([]byte{'x'}, 3000))}
	}
	if _, err = Seal(b, "isolated-backup-pass"); err == nil {
		t.Fatal("oversized runtime accepted")
	}
}
