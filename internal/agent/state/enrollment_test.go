package state

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEnrollmentSecretPersistsAcrossRestartAndChangesOnlyForNewToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "credentials.json")
	s := NewCredentialStore(path)
	first, err := s.EnrollmentSecret("https://panel.example.test", "enr_test_one")
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewCredentialStore(path)
	second, err := restarted.EnrollmentSecret("https://panel.example.test", "enr_test_one")
	if err != nil || first != second {
		t.Fatal("restart lost private enrollment attempt")
	}
	data, err := os.ReadFile(s.enrollmentPath())
	if err != nil || bytes.Contains(data, []byte("enr_test_one")) {
		t.Fatal("attempt retained raw token")
	}
	if _, err := s.EnrollmentSecret("https://other.example.test", "enr_test_one"); err == nil {
		t.Fatal("attempt rebound to another panel")
	}
	third, err := s.EnrollmentSecret("https://panel.example.test", "enr_test_two")
	if err != nil || third == first {
		t.Fatalf("new token failed to create distinct attempt: %v", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(s.enrollmentPath(), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := s.EnrollmentSecret("https://panel.example.test", "enr_test_two"); err == nil {
			t.Fatal("public attempt accepted")
		}
	}
	if err := s.ClearEnrollmentAttempt(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.enrollmentPath()); !os.IsNotExist(err) {
		t.Fatal("completed attempt was not removed")
	}
}
