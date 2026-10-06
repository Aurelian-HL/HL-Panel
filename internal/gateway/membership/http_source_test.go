package membership

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHTTPSourceAuthenticatedSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-token" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = writer.Write([]byte(`{"revision":3,"endpoints":[]}`))
	}))
	defer server.Close()
	source, err := NewHTTPSource(server.URL, tokenFile(t))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := source.Load(context.Background())
	if err != nil || snapshot.Revision != 3 || snapshot.Endpoints == nil || snapshot.SHA256 == "" {
		t.Fatalf("Load() = %#v, %v", snapshot, err)
	}
}

func TestHTTPSourceRejectsUnsafeURLsAndResponses(t *testing.T) {
	tokenPath := tokenFile(t)
	for _, rawURL := range []string{"http://example.com/members", "http://user:pass@localhost/members", "http://localhost/members?token=secret", "file:///members"} {
		if _, err := NewHTTPSource(rawURL, tokenPath); err == nil {
			t.Fatalf("accepted unsafe membership URL %q", rawURL)
		}
	}
	other := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"revision":1,"endpoints":[]}`))
	}))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/redirect":
			http.Redirect(writer, request, other.URL, http.StatusFound)
		case "/oversize":
			_, _ = writer.Write([]byte(strings.Repeat("x", int(MaxSnapshotBytes)+1)))
		case "/invalid":
			_, _ = writer.Write([]byte(`{"revision":1,"endpoints":[],"unknown":1}`))
		default:
			writer.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer server.Close()
	for _, path := range []string{"/redirect", "/oversize", "/invalid", "/unauthorized"} {
		source, err := NewHTTPSource(server.URL+path, tokenPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := source.Load(context.Background()); err == nil {
			t.Fatalf("Load(%s) accepted unsafe response", path)
		}
	}
}

func tokenFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gateway-token")
	if err := os.WriteFile(path, []byte("test-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
