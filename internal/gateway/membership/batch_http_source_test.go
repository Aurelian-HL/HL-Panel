package membership

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBatchSourceSharesRequestsAndDoesNotExtendLeases(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	lease := time.Now().Add(-time.Second).UTC()
	tokens := map[string]string{"one": strings.Repeat("a", 32), "two": strings.Repeat("b", 32)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var got map[string]string
		if r.Method != "POST" || json.NewDecoder(r.Body).Decode(&got) != nil || got["one"] != tokens["one"] || got["two"] != tokens["two"] {
			w.WriteHeader(401)
			return
		}
		if fail.Load() {
			w.WriteHeader(503)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"one": map[string]any{"revision": 3, "endpoints": []any{map[string]any{"id": "backend", "address": "127.0.0.1:9999", "weight": 1, "status": "ready", "health_lease_expires_at": lease}}}, "two": map[string]any{"revision": 3, "endpoints": []any{}}})
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "tokens.json")
	raw, _ := json.Marshal(tokens)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := NewBatchHTTPSource(server.URL, path, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "one"
			if i%2 == 0 {
				id = "two"
			}
			snap, err := (BatchPoolSource{s, id}).Load(context.Background())
			if err != nil || snap.Revision != 3 {
				t.Errorf("load failed: %v", err)
			}
			if id == "one" && !snap.Endpoints[0].HealthLeaseExpiresAt.Equal(lease) {
				t.Error("lease changed")
			}
		}(i)
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("requests=%d want 1", calls.Load())
	}
	fail.Store(true)
	s.mu.Lock()
	s.refreshed = time.Time{}
	s.mu.Unlock()
	if _, err := (BatchPoolSource{s, "one"}).Load(context.Background()); err == nil {
		t.Fatal("failed refresh returned stale success")
	}
	if _, err := (BatchPoolSource{s, "unauthorized"}).Load(context.Background()); err == nil {
		t.Fatal("uncredentialed pool accepted")
	}
	if calls.Load() != 2 {
		t.Fatal("failure must also be shared")
	}
}
