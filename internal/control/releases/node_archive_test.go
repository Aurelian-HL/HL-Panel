package releases

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type archiveTransport func(*http.Request) (*http.Response, error)

func (f archiveTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestNodeArchiveRequiresOfficialDigestAndCompatibleVersion(t *testing.T) {
	s := New("v0.1.123")
	body := fmt.Sprintf(`{"tag_name":"v0.1.123","published_at":"2026-01-01T00:00:00Z","assets":[{"name":"hl-panel-linux-amd64.tar.gz","browser_download_url":"%s/releases/download/v0.1.123/hl-panel-linux-amd64.tar.gz","digest":"sha256:%s"}]}`, RepositoryURL, strings.Repeat("a", 64))
	s.client.Transport = archiveTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.github.com/repos/Aurelian-HL/HL-Panel/releases/tags/v0.1.123" || r.Header.Get("Authorization") != "" {
			t.Fatal("unexpected destination or credentials")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	for _, current := range []string{"v0.1.122", "v0.1.123"} {
		if sha, err := s.ResolveNodeArchive(context.Background(), "v0.1.123", current); err != nil || sha != strings.Repeat("a", 64) {
			t.Fatal(sha, err)
		}
	}
	for _, current := range []string{"v0.1.124", "dev"} {
		if _, err := s.ResolveNodeArchive(context.Background(), "v0.1.123", current); err == nil {
			t.Fatal("downgrade accepted")
		}
	}
	for _, bad := range []string{strings.Replace(body, "sha256:", "bad:", 1), strings.Replace(body, "github.com/Aurelian-HL", "evil.test/Aurelian-HL", 1), strings.Replace(body, `"assets":`, `"draft":true,"assets":`, 1)} {
		saved := body
		body = bad
		if _, err := s.ResolveNodeArchive(context.Background(), "v0.1.123", "v0.1.122"); err == nil {
			t.Fatal("unverified release accepted")
		}
		body = saved
	}
}
