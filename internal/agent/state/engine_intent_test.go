package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEngineIntentPersistsWithoutChangingLegacyJournal(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state.json")
	store := NewAtomicStateStore(path, filepath.Join(root, "configurations"))
	if stopped, err := store.EngineStopped(); err != nil || stopped {
		t.Fatalf("initial intent = %v, %v", stopped, err)
	}
	for _, want := range []bool{true, false, true} {
		if err := store.SetEngineStopped(want); err != nil {
			t.Fatal(err)
		}
		reopened := NewAtomicStateStore(path, filepath.Join(root, "configurations"))
		if got, err := reopened.EngineStopped(); err != nil || got != want {
			t.Fatalf("persisted intent = %v, %v; want %v", got, err, want)
		}
		if _, err := reopened.Load(); err != nil {
			t.Fatalf("legacy journal cannot load: %v", err)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("intent writes changed the legacy journal")
	}
	if err := os.WriteFile(path+".engine-intent", []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EngineStopped(); err == nil {
		t.Fatal("invalid operator intent silently restarted the engine")
	}
}
