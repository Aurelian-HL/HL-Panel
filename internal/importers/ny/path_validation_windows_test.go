//go:build windows

package ny

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsPathValidationRejectsReservedDevicesAndADS(t *testing.T) {
	directory := t.TempDir()
	regular := filepath.Join(directory, "snapshot.json")
	if err := os.WriteFile(regular, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(directory, "NUL.json"),
		filepath.Join(directory, "COM1.txt"),
		regular + ":hidden-stream",
	} {
		if _, err := validateLocalSnapshotPath(path); !errors.Is(err, ErrSnapshotPathDevice) {
			t.Fatalf("validateLocalSnapshotPath(%q) error = %v, want ErrSnapshotPathDevice", path, err)
		}
		if _, err := ValidateNewLocalFilePath(path); !errors.Is(err, ErrSnapshotPathDevice) {
			t.Fatalf("ValidateNewLocalFilePath(%q) error = %v, want ErrSnapshotPathDevice", path, err)
		}
	}
}
