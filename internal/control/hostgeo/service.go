package hostgeo

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const maxCacheEntries = 4096

type cacheEntry struct {
	country string
	retryAt time.Time
	loading bool
}

type Service struct {
	mu       sync.Mutex
	cache    map[string]cacheEntry
	workers  chan struct{}
	client   *http.Client
	endpoint string
	now      func() time.Time
}

func New() *Service {
	return &Service{
		cache: make(map[string]cacheEntry), workers: make(chan struct{}, 4),
		client: &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		endpoint: "https://ipwho.is/", now: time.Now,
	}
}

// Country returns cached geography immediately. A bounded background lookup
// ensures a provider outage cannot delay heartbeats or the monitoring API.
func (s *Service) Country(address string) string {
	ip := PublicIP(address)
	if ip == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, exists := s.cache[ip]
	if entry.loading || s.now().Before(entry.retryAt) {
		return entry.country
	}
	select {
	case s.workers <- struct{}{}:
	default:
		return entry.country
	}
	if !exists && len(s.cache) >= maxCacheEntries {
		oldestIP := ""
		var oldest time.Time
		for key, candidate := range s.cache {
			if !candidate.loading && (oldestIP == "" || candidate.retryAt.Before(oldest)) {
				oldestIP, oldest = key, candidate.retryAt
			}
		}
		delete(s.cache, oldestIP)
	}
	entry.loading = true
	s.cache[ip] = entry
	go s.lookup(ip)
	return entry.country
}

func (s *Service) lookup(ip string) {
	defer func() { <-s.workers }()
	country := s.fetch(ip)
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.cache[ip]
	entry.loading = false
	entry.retryAt = s.now().Add(5 * time.Minute)
	if country != "" {
		entry.country = country
		entry.retryAt = s.now().Add(24 * time.Hour)
	}
	s.cache[ip] = entry
}

func (s *Service) fetch(ip string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.endpoint+ip+"?fields=success,ip,country_code", nil)
	if err != nil {
		return ""
	}
	request.Header.Set("User-Agent", "HL-panel-host-geoip")
	response, err := s.client.Do(request)
	if err != nil {
		return ""
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(raw) > 4096 {
		return ""
	}
	var result struct {
		Success bool   `json:"success"`
		IP      string `json:"ip"`
		Country string `json:"country_code"`
	}
	if json.Unmarshal(raw, &result) != nil || !result.Success || PublicIP(result.IP) != ip {
		return ""
	}
	country := strings.ToUpper(result.Country)
	if len(country) != 2 || country[0] < 'A' || country[0] > 'Z' || country[1] < 'A' || country[1] > 'Z' {
		return ""
	}
	return country
}
