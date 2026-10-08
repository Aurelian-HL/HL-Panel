package panelruntime

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLogStoreRealLoggerPersistenceAndPrivacy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.log")
	store := NewLogStore(path)
	logger := slog.New(slog.NewJSONHandler(store, nil))
	logger.Info("面板启动", "password", "hidden-attribute", "error", "hidden-error")
	logger.Warn("bearer abc-secret token=abc-secret enr_example-secret postgres://private:private@db/name")
	result := store.Read(100)
	if len(result.Items) != 2 || !result.Persistent {
		t.Fatalf("missing real logs: %+v", result)
	}
	encoded, _ := json.Marshal(result)
	for _, secret := range []string{"hidden-attribute", "hidden-error", "abc-secret", "example-secret", "private"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("exposed secret %s", secret)
		}
	}
	restored := NewLogStore(path).Read(1)
	if len(restored.Items) != 1 || restored.Items[0].Level != "WARN" {
		t.Fatalf("restart failed: %+v", restored)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("unsafe mode %v", info.Mode())
	}
	result.Items[0].Message = "caller modified"
	if store.Read(100).Items[0].Message != "面板启动" {
		t.Fatal("caller mutated store")
	}
}

func TestLogStoreBoundsUnicodeAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.log")
	var input strings.Builder
	for i := 0; i < 2100; i++ {
		data, _ := json.Marshal(map[string]string{"time": "2026-10-08T00:00:00Z", "level": "INFO", "msg": strings.Repeat("中<>", 900)})
		input.Write(data)
		input.WriteByte('\n')
	}
	store := NewLogStore(path)
	store.Write([]byte(input.String()))
	info, err := os.Stat(path)
	if err != nil || info.Size() > maxLogBytes {
		t.Fatalf("oversized snapshot: %v %v", info, err)
	}
	result := store.Read(5000)
	if len(result.Items) == 0 || len(result.Items) > 2000 {
		t.Fatal("entry bound violated")
	}
	for _, entry := range result.Items {
		if !utf8.ValidString(entry.Message) || len(entry.Message) > 2048 {
			t.Fatal("invalid unicode")
		}
	}
	if got := NewLogStore(path).Read(5000); len(got.Items) != len(result.Items) {
		t.Fatalf("restart lost %d logs", len(result.Items)-len(got.Items))
	}
	if len(store.Read(0).Items) > 100 {
		t.Fatal("default limit not applied")
	}
}

func TestLogStoreRejectsUnsafeFilesAndFallsBack(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "private")
	os.WriteFile(target, []byte("do not overwrite"), 0600)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err == nil {
		store := NewLogStore(link)
		slog.New(slog.NewJSONHandler(store, nil)).Error("test")
		if store.Read(1).Persistent {
			t.Fatal("followed symlink")
		}
		body, _ := os.ReadFile(target)
		if string(body) != "do not overwrite" {
			t.Fatal("target overwritten")
		}
	}
	store := NewLogStore(filepath.Join(dir, "missing", "panel.log"))
	slog.New(slog.NewJSONHandler(store, nil)).Info("本次运行")
	if got := store.Read(1); got.Persistent || len(got.Items) != 1 {
		t.Fatalf("fallback failed: %+v", got)
	}
	store.Write([]byte("not-json\n{\"time\":\"invalid\",\"level\":\"INFO\",\"msg\":\"invalid\"}\n"))
	if len(store.Read(100).Items) != 1 {
		t.Fatal("malformed record accepted")
	}
}
