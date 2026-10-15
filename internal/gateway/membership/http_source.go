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

const (
	maxTokenBytes             = 4096
	membershipRequestTimeout  = 5 * time.Second
	membershipRequestAttempts = 2
	membershipRetryBackoff    = 250 * time.Millisecond
)

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
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// The gateway refreshes frequently. Keep pooled connections shorter-lived
	// than the reverse proxy idle timeout so a refresh does not reuse a socket
	// that the proxy has already discarded.
	transport.MaxIdleConns = 8
	transport.MaxIdleConnsPerHost = 4
	transport.MaxConnsPerHost = 8
	transport.IdleConnTimeout = 20 * time.Second
	transport.TLSHandshakeTimeout = 5 * time.Second
	transport.ResponseHeaderTimeout = membershipRequestTimeout
	transport.ExpectContinueTimeout = time.Second
	transport.DialContext = (&net.Dialer{Timeout: membershipRequestTimeout, KeepAlive: 30 * time.Second}).DialContext
	return &HTTPSource{
		url:   rawURL,
		token: token,
		client: &http.Client{Timeout: membershipRequestTimeout, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("membership redirect refused")
		}},
	}, nil
}

func (s *HTTPSource) Load(ctx context.Context) (Snapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var lastErr error
	for attempt := 0; attempt < membershipRequestAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return Snapshot{}, err
		}
		requestContext, cancel := context.WithTimeout(ctx, membershipRequestTimeout)
		snapshot, retry, err := s.loadOnce(requestContext)
		cancel()
		if err == nil {
			return snapshot, nil
		}
		lastErr = err
		if !retry || attempt+1 >= membershipRequestAttempts {
			return Snapshot{}, err
		}
		timer := time.NewTimer(membershipRetryBackoff)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return Snapshot{}, ctx.Err()
		}
	}
	return Snapshot{}, lastErr
}

func (s *HTTPSource) loadOnce(ctx context.Context) (Snapshot, bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return Snapshot{}, false, err
	}
	request.Header.Set("Authorization", "Bearer "+s.token)
	request.Header.Set("Accept", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return Snapshot{}, true, fmt.Errorf("fetch membership snapshot: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return Snapshot{}, response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= http.StatusInternalServerError,
			fmt.Errorf("membership endpoint returned HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, MaxSnapshotBytes+1))
	if err != nil {
		return Snapshot{}, true, fmt.Errorf("read membership response: %w", err)
	}
	snapshot, err := DecodeSnapshot(raw)
	if err != nil {
		return Snapshot{}, false, err
	}
	return snapshot, false, nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
