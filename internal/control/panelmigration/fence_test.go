package panelmigration

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

const testID = "12345678-1234-1234-1234-123456789abc"

func TestDurableFenceBlocksEveryBusinessSurface(t *testing.T) {
	directory := t.TempDir()
	os.Chmod(directory, 0700)
	fence, err := OpenFence(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err = fence.Set(FenceState{Role: "source", ID: testID}); err != nil {
		t.Fatal(err)
	}
	fence, err = OpenFence(directory)
	if err != nil || !fence.Active() {
		t.Fatal("fence did not survive restart", err)
	}
	for _, path := range []string{"/api/v1/nodes/enroll", "/api/v1/forward-rules", "/api/v1/customer/subscriptions", "/api/v1/public/subscriptions/token", "/api/v1/auth/password", "/api/v1/panel/update", "/api/v1/panel/migration/import"} {
		if fence.Allows(httptest.NewRequest(http.MethodPost, path, nil)) {
			t.Fatalf("business allowed: %s", path)
		}
	}
	for _, path := range []string{"/healthz", "/api/v1/auth/login", "/api/v1/auth/me", "/api/v1/panel/migration/auto", "/api/v1/panel/migration/recovery/mbk_0123456789abcdef0123456789abcdef"} {
		if !fence.Allows(httptest.NewRequest(http.MethodGet, path, nil)) {
			t.Fatalf("recovery blocked: %s", path)
		}
	}
	if fence.Clear("different-task") == nil || !fence.Active() {
		t.Fatal("foreign task lifted fence")
	}
	if err := fence.Clear(testID); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFence(directory)
	if err != nil || reopened.Active() {
		t.Fatal("cleared fence survived", err)
	}
}

func TestReceiverAllowsOnlyDirectLoopbackRestore(t *testing.T) {
	directory := t.TempDir()
	os.Chmod(directory, 0700)
	if err := writePrivate(filepath.Join(directory, "fence.json"), FenceState{Role: "receiver", ID: testID}); err != nil {
		t.Fatal(err)
	}
	fence, err := OpenFence(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/auth/login", "/api/v1/panel/migration/preview", "/api/v1/panel/migration/import"} {
		request := httptest.NewRequest(http.MethodPost, path, nil)
		request.RemoteAddr = "127.0.0.1:10000"
		if !fence.Allows(request) {
			t.Fatal("local restoration blocked")
		}
		request.Header.Set("X-Forwarded-For", "8.8.8.8")
		if fence.Allows(request) {
			t.Fatal("nginx bypassed receiver fence")
		}
		request.Header.Del("X-Forwarded-For")
		request.Header.Set("Forwarded", "for=8.8.8.8")
		if fence.Allows(request) {
			t.Fatal("forwarded receiver request accepted")
		}
		request.Header.Del("Forwarded")
		request.RemoteAddr = "8.8.8.8:10000"
		if fence.Allows(request) {
			t.Fatal("public receiver request accepted")
		}
	}
	if fence.Allows(httptest.NewRequest(http.MethodGet, "/api/v1/panel/migration/auto", nil)) {
		t.Fatal("receiver enabled remote management")
	}
	if fence.Clear(testID) == nil {
		t.Fatal("receiver lifted by source action")
	}
}

func TestCorruptMetadataFailsClosed(t *testing.T) {
	directory := t.TempDir()
	os.Chmod(directory, 0700)
	path := filepath.Join(directory, "fence.json")
	os.WriteFile(path, []byte(`{"role":"unknown","id":"bad"}`), 0600)
	if _, err := OpenFence(directory); err == nil {
		t.Fatal("invalid fence accepted")
	}
	if runtime.GOOS != "windows" {
		os.WriteFile(path, []byte(`{}`), 0644)
		os.Chmod(path, 0644)
		if _, err := readPrivate(path); err == nil {
			t.Fatal("public metadata accepted")
		}
	}
}
