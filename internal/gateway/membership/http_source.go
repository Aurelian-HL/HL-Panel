package membership

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/securefile"
)

const maxTokenBytes = 4096

type HTTPSource struct {
	url    string
	token  string
	client *http.Client
}

// NewHTTPSource accepts only authenticated HTTPS or loopback HTTP. Redirects
// are disabled so the bearer credential cannot be forwarded to another host.
func NewHTTPSource(rawURL, tokenFile string) (*HTTPSource, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed == nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("membership_url must be an absolute URL without credentials, query, or fragment")
	}
	if parsed.Scheme != "https" && (parsed.Scheme != "http" || !isLoopback(parsed.Hostname())) {
		return nil, errors.New("membership_url must use HTTPS or loopback HTTP")
	}
	if !filepath.IsAbs(tokenFile) {
		return nil, errors.New("membership_token_file must be an absolute path")
	}
	raw, err := securefile.ReadBoundedRegular(tokenFile, maxTokenBytes)
	if err != nil {
		return nil, fmt.Errorf("read membership token: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return nil, errors.New("membership token is invalid")
	}
	return &HTTPSource{
		url:   rawURL,
		token: token,
		client: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("membership redirect refused")
		}},
	}, nil
}

func (s *HTTPSource) Load(ctx context.Context) (Snapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return Snapshot{}, err
	}
	request.Header.Set("Authorization", "Bearer "+s.token)
	request.Header.Set("Accept", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return Snapshot{}, fmt.Errorf("fetch membership snapshot: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Snapshot{}, fmt.Errorf("membership endpoint returned HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, MaxSnapshotBytes+1))
	if err != nil {
		return Snapshot{}, fmt.Errorf("read membership response: %w", err)
	}
	return DecodeSnapshot(raw)
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
