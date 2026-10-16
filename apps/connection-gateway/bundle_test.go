package main

import (
	"context"
	"encoding/json"
	"github.com/hongle/hl-panel/internal/gateway/membership"
	"github.com/hongle/hl-panel/internal/routing/endpointrouter"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBundleTwoListenersShareMembershipAndBalanceBackends(t *testing.T) {
	var backendCounts [2]atomic.Int32
	endpoints := make([]endpointrouter.Endpoint, 2)
	for i := 0; i < 2; i++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		endpoints[i] = endpointrouter.Endpoint{ID: []string{"a", "b"}[i], Address: l.Addr().String(), Weight: []int{3, 2}[i], Status: endpointrouter.EndpointReady, HealthLeaseExpiresAt: time.Now().Add(time.Minute)}
		go func(i int, l net.Listener) {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				backendCounts[i].Add(1)
				c.Write([]byte("ok"))
				c.Close()
			}
		}(i, l)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(map[string]membership.Snapshot{"one": {Revision: 1, Endpoints: endpoints}, "two": {Revision: 1, Endpoints: endpoints}})
	}))
	defer server.Close()
	dir := t.TempDir()
	tokens := filepath.Join(dir, "tokens")
	writeJSONFile(t, tokens, map[string]string{"one": strings.Repeat("a", 32), "two": strings.Repeat("b", 32)})
	addresses := []string{unusedLoopbackAddress(t), unusedLoopbackAddress(t)}
	config := filepath.Join(dir, "bundle.json")
	writeJSONFile(t, config, map[string]any{"defaults": map[string]any{"membership_url": server.URL, "membership_token_file": tokens, "membership_refresh_interval": "5s", "dial_timeout": "1s"}, "listeners": []any{map[string]string{"pool_id": "one", "listen_address": addresses[0]}, map[string]string{"pool_id": "two", "listen_address": addresses[1]}}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runBundle(ctx, config, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	for _, address := range addresses {
		for i := 0; i < 10; i++ {
			c := dialWhenReady(t, address, done)
			c.SetDeadline(time.Now().Add(time.Second))
			raw, err := io.ReadAll(c)
			c.Close()
			if err != nil || string(raw) != "ok" {
				t.Fatalf("backend response: %q %v", raw, err)
			}
		}
	}
	if backendCounts[0].Load() != 12 || backendCounts[1].Load() != 8 || calls.Load() != 1 {
		t.Fatalf("balance=%d/%d batch requests=%d", backendCounts[0].Load(), backendCounts[1].Load(), calls.Load())
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bundle did not stop")
	}
}
