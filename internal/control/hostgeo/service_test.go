package hostgeo

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublicIP(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.0.1", "169.254.1.1", "100.64.1.2", "0.1.2.3", "198.51.100.1", "::1", "fe80::1", "fc00::1", "2001:db8::1", "::ffff:10.0.0.1", "224.1.1.1", "255.255.255.255", "example.com", "1.1.1.1:443", "1.1.1.1/anything", ""} {
		if PublicIP(value) != "" {
			t.Errorf("nonpublic or nonliteral address accepted: %s", value)
		}
	}
	for input, want := range map[string]string{"8.8.8.8": "8.8.8.8", " 1.1.1.1 ": "1.1.1.1", "::ffff:8.8.8.8": "8.8.8.8", "2606:4700:4700::1111": "2606:4700:4700::1111"} {
		if got := PublicIP(input); got != want {
			t.Errorf("PublicIP(%s) = %s, want %s", input, got, want)
		}
	}
}

func waitForLookup(t *testing.T, service *Service, ip string) cacheEntry {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		service.mu.Lock()
		entry, exists := service.cache[ip]
		service.mu.Unlock()
		if exists && !entry.loading {
			return entry
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("GeoIP lookup did not finish")
	return cacheEntry{}
}

func TestAsyncLookupDeduplicatesCachesAndRetainsLastGood(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		if r.URL.Path != "/8.8.8.8" || r.URL.Query().Get("fields") != "success,ip,country_code" || r.Header.Get("Authorization") != "" {
			t.Error("unexpected geography request")
		}
		if fail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"success":true,"ip":"8.8.8.8","country_code":"us"}`)
	}))
	defer server.Close()
	service := New()
	service.endpoint = server.URL + "/"
	if got := service.Country("8.8.8.8"); got != "" {
		t.Fatal("uncached geography was fabricated")
	}
	<-started
	for range 20 {
		service.Country("8.8.8.8")
	}
	close(release)
	entry := waitForLookup(t, service, "8.8.8.8")
	if entry.country != "US" || calls.Load() != 1 || entry.retryAt.Before(time.Now().Add(23*time.Hour)) {
		t.Fatalf("wrong success cache: %+v, requests=%d", entry, calls.Load())
	}
	service.Country("8.8.8.8")
	if calls.Load() != 1 {
		t.Fatal("cached geography was queried again")
	}
	fail.Store(true)
	service.mu.Lock()
	entry.retryAt = time.Time{}
	service.cache["8.8.8.8"] = entry
	service.mu.Unlock()
	if got := service.Country("8.8.8.8"); got != "US" {
		t.Fatal("refresh hid last known good geography")
	}
	entry = waitForLookup(t, service, "8.8.8.8")
	if entry.country != "US" || calls.Load() != 2 || entry.retryAt.Before(time.Now().Add(4*time.Minute)) || entry.retryAt.After(time.Now().Add(6*time.Minute)) {
		t.Fatalf("wrong failed-refresh cache: %+v", entry)
	}
}

func TestRejectsProviderFailuresMismatchedIPAndBadCountry(t *testing.T) {
	for _, body := range []string{
		`{"success":false,"ip":"8.8.8.8","country_code":"US"}`,
		`{"success":true,"ip":"1.1.1.1","country_code":"US"}`,
		`{"success":true,"ip":"8.8.8.8","country_code":"USA"}`,
		`{"success":true,"ip":"8.8.8.8","country_code":"U1"}`,
		`invalid json`,
	} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer server.Close()
			service := New()
			service.endpoint = server.URL + "/"
			service.Country("8.8.8.8")
			if entry := waitForLookup(t, service, "8.8.8.8"); entry.country != "" {
				t.Fatal("invalid provider result accepted")
			}
		})
	}
}

func TestLookupConcurrencyAndCacheAreBounded(t *testing.T) {
	service := New()
	for range cap(service.workers) {
		service.workers <- struct{}{}
	}
	service.Country("8.8.8.8")
	if len(service.cache) != 0 {
		t.Fatal("queued work exceeded worker bound")
	}
	for range cap(service.workers) {
		<-service.workers
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	service.endpoint = server.URL + "/"
	for i := range maxCacheEntries {
		service.cache[fmt.Sprint(i)] = cacheEntry{retryAt: time.Now()}
	}
	service.Country("8.8.8.8")
	waitForLookup(t, service, "8.8.8.8")
	if len(service.cache) != maxCacheEntries {
		t.Fatal("cache exceeded its bound")
	}
	before := len(service.cache)
	service.Country("http://127.0.0.1/private")
	service.Country("10.1.2.3")
	if len(service.cache) != before {
		t.Fatal("invalid addresses scheduled lookups")
	}
}
