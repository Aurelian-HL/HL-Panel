package membership

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hongle/hl-panel/internal/securefile"
)

// BatchHTTPSource shares one bounded request and cache across listener pools.
// Only the control plane's original leases authorize endpoints; caching never
// extends them. Failed refreshes are returned to the runtime's LKG policy.
type BatchHTTPSource struct {
	mu        sync.Mutex
	url       string
	body      []byte
	tokens    map[string]string
	client    *http.Client
	interval  time.Duration
	refreshed time.Time
	snapshots map[string]Snapshot
	lastError error
}

func NewBatchHTTPSource(rawURL, tokenFile string, interval time.Duration) (*BatchHTTPSource, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Scheme != "https" && (parsed.Scheme != "http" || !isLoopback(parsed.Hostname()))) {
		return nil, errors.New("batch membership requires HTTPS or loopback HTTP without credentials, query, or fragment")
	}
	if !filepath.IsAbs(tokenFile) || interval < 100*time.Millisecond || interval > 5*time.Minute {
		return nil, errors.New("invalid batch membership file or refresh interval")
	}
	raw, err := securefile.ReadBoundedRegular(tokenFile, 192<<10)
	if err != nil {
		return nil, fmt.Errorf("read batch membership credentials: %w", err)
	}
	var tokens map[string]string
	if json.Unmarshal(raw, &tokens) != nil || len(tokens) == 0 || len(tokens) > 512 {
		return nil, errors.New("invalid batch pool credentials")
	}
	for id, token := range tokens {
		if id == "" || strings.TrimSpace(id) != id || len(token) < 32 || len(token) > 256 || strings.ContainsAny(token, " \t\r\n") {
			return nil, errors.New("invalid batch pool credential")
		}
	}
	body, _ := json.Marshal(tokens)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.MaxConnsPerHost = 2
	transport.IdleConnTimeout = 20 * time.Second
	transport.ResponseHeaderTimeout = membershipRequestTimeout
	return &BatchHTTPSource{url: rawURL, body: body, tokens: tokens, interval: interval, client: &http.Client{Transport: transport, Timeout: membershipRequestTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("membership redirect refused") }}}, nil
}

type BatchPoolSource struct {
	Source *BatchHTTPSource
	PoolID string
}

func (s BatchPoolSource) Load(ctx context.Context) (Snapshot, error) {
	return s.Source.load(ctx, s.PoolID)
}

func (s *BatchHTTPSource) load(ctx context.Context, id string) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tokens[id]; !ok {
		return Snapshot{}, errors.New("pool has no membership credential")
	}
	if s.refreshed.IsZero() || time.Since(s.refreshed) >= s.interval {
		snapshots, err := s.fetch(ctx)
		s.refreshed = time.Now()
		s.lastError = err
		if err == nil {
			s.snapshots = snapshots
		}
	}
	if s.lastError != nil {
		return Snapshot{}, s.lastError
	}
	snapshot, ok := s.snapshots[id]
	if !ok {
		return Snapshot{}, errors.New("pool absent from membership response")
	}
	return snapshot, nil
}

func (s *BatchHTTPSource) fetch(ctx context.Context) (map[string]Snapshot, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(s.body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return nil, errors.New("batch membership request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("batch membership returned HTTP %d", response.StatusCode)
	}
	const maxBytes = 2 << 20
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil || len(raw) > maxBytes {
		return nil, errors.New("invalid batch membership response size")
	}
	var items map[string]json.RawMessage
	if json.Unmarshal(raw, &items) != nil || items == nil || len(items) > len(s.tokens) {
		return nil, errors.New("invalid batch membership response pools")
	}
	result := make(map[string]Snapshot, len(items))
	for id, raw := range items {
		if _, ok := s.tokens[id]; !ok {
			return nil, errors.New("unexpected membership pool")
		}
		snapshot, err := DecodeSnapshot(raw)
		if err != nil {
			return nil, errors.New("invalid pool membership snapshot")
		}
		result[id] = snapshot
	}
	return result, nil
}
