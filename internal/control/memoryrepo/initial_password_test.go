package memoryrepo

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
)

func TestLegacyAdministratorSnapshotPreservesCustomPassword(t *testing.T) {
	hash, err := auth.HashPassword("legacy-custom-secret")
	if err != nil {
		t.Fatal(err)
	}
	store := New(auth.Administrator{ID: "adm_legacy", Username: "existing", PasswordHash: hash, CreatedAt: time.Now()})
	raw, err := store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var old map[string]any
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	old["Version"] = 15
	delete(old["Administrators"].(map[string]any)["existing"].(map[string]any), "MustChangePassword")
	raw, _ = json.Marshal(old)
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := restored.AdministratorByUsername(t.Context(), "existing")
	if err != nil || admin.MustChangePassword || string(admin.PasswordHash) != string(hash) {
		t.Fatal("legacy administrator changed")
	}
}
