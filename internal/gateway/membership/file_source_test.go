package membership

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFileSourceLoadsStrictVersionedSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "members.json")
	raw := []byte("{\"revision\":7,\"endpoints\":[{\"id\":\"node-a\",\"address\":\"127.0.0.1:8443\",\"weight\":2,\"status\":\"ready\",\"health_lease_expires_at\":\"2099-01-01T00:00:00Z\"}]}")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := NewFileSource(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := source.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision != 7 || len(snapshot.Endpoints) != 1 || snapshot.Endpoints[0].ID != "node-a" || len(snapshot.SHA256) != 64 {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
}

func TestFileSourceRejectsUnknownFieldsAndMissingArray(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown":  "{\"revision\":1,\"endpoints\":[],\"secret\":\"no\"}",
		"missing":  "{\"revision\":1}",
		"zero":     "{\"revision\":0,\"endpoints\":[]}",
		"trailing": "{\"revision\":1,\"endpoints\":[]} {}",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "members.json")
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			source, err := NewFileSource(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := source.Load(context.Background()); err == nil {
				t.Fatal("Load() accepted invalid membership snapshot")
			}
		})
	}
}

func TestFileSourceRequiresAbsolutePathAndHonorsContext(t *testing.T) {
	if _, err := NewFileSource("members.json"); err == nil {
		t.Fatal("NewFileSource() accepted relative path")
	}
	path := filepath.Join(t.TempDir(), "members.json")
	if err := os.WriteFile(path, []byte("{\"revision\":1,\"endpoints\":[]}"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := NewFileSource(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.Load(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Load() error = %v, want context.Canceled", err)
	}
}
