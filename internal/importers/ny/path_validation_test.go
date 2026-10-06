package ny

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestReadSnapshotAcceptsAbsoluteLocalRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	content := []byte(`{"fixture":true}`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	inspector := newTestInspector(t)
	snapshot, err := inspector.ReadSnapshot(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadSnapshot() error = %v", err)
	}
	if string(snapshot.RawCopy()) != string(content) {
		t.Fatal("ReadSnapshot() changed the local source bytes")
	}
}

func TestValidateNewLocalFilePathAcceptsMissingFinalFile(t *testing.T) {
	want := filepath.Join(t.TempDir(), "new-report.json")
	got, err := ValidateNewLocalFilePath(want)
	if err != nil {
		t.Fatalf("ValidateNewLocalFilePath() error = %v", err)
	}
	if got != want {
		t.Fatalf("ValidateNewLocalFilePath() = %q, want %q", got, want)
	}
}

func TestReadSnapshotRejectsRelativeUNCAndDeviceNamespacesBeforeOpen(t *testing.T) {
	inspector := newTestInspector(t)
	tests := []struct {
		name string
		path string
		want error
	}{
		{name: "relative", path: "snapshot.json", want: ErrSnapshotPathAbsolute},
		{name: "UNC", path: `\\definitely-not-a-real-host.invalid\share\snapshot.json`, want: ErrSnapshotPathRemote},
		{name: "Win32 device", path: `\\.\NUL`, want: ErrSnapshotPathDevice},
		{name: "extended namespace", path: `\\?\C:\snapshots\export.json`, want: ErrSnapshotPathDevice},
		{name: "NT device", path: `\??\C:\snapshots\export.json`, want: ErrSnapshotPathDevice},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := inspector.ReadSnapshot(context.Background(), test.path)
			if !errors.Is(err, test.want) {
				t.Fatalf("ReadSnapshot(%q) error = %v, want %v", test.path, err, test.want)
			}
		})
	}
}

func TestReadSnapshotRejectsSymlinkOrReparseComponent(t *testing.T) {
	root := t.TempDir()
	realDirectory := filepath.Join(root, "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatalf("Mkdir(real): %v", err)
	}
	if err := os.WriteFile(filepath.Join(realDirectory, "snapshot.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	linkDirectory := filepath.Join(root, "link")
	if err := os.Symlink(realDirectory, linkDirectory); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("creating a Windows symlink requires developer mode or privilege: %v", err)
		}
		t.Fatalf("Symlink(): %v", err)
	}
	inspector := newTestInspector(t)
	_, err := inspector.ReadSnapshot(context.Background(), filepath.Join(linkDirectory, "snapshot.json"))
	if !errors.Is(err, ErrSnapshotPathReparse) {
		t.Fatalf("ReadSnapshot(symlink path) error = %v, want ErrSnapshotPathReparse", err)
	}
	_, err = ValidateNewLocalFilePath(filepath.Join(linkDirectory, "new-report.json"))
	if !errors.Is(err, ErrSnapshotPathReparse) {
		t.Fatalf("ValidateNewLocalFilePath(symlink parent) error = %v, want ErrSnapshotPathReparse", err)
	}
}
