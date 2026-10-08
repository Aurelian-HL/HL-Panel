package controlclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDesiredWatchCompatibilityAndTimeout(t *testing.T) {
	for _, supported := range []bool{true, false} {
		t.Run(map[bool]string{true: "new-panel", false: "old-panel"}[supported], func(t *testing.T) {
			ordinary := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer test-node-secret" {
					t.Error("watch not authenticated")
				}
				if r.URL.Path == "/api/v1/agent/desired-watch" {
					if r.URL.Query().Get("wait_ms") != "1000" {
						t.Error("watch exceeded client timeout budget")
					}
					if !supported {
						w.WriteHeader(404)
						return
					}
				} else if r.URL.Path == "/api/v1/agent/desired" {
					ordinary++
				} else {
					t.Error("wrong endpoint")
				}
				w.WriteHeader(204)
			}))
			defer server.Close()
			httpClient := server.Client()
			httpClient.Timeout = 2 * time.Second
			client, err := NewWithOptions(server.URL, httpClient, true)
			if err != nil {
				t.Fatal(err)
			}
			desired, actual, err := client.DesiredWatch(context.Background(), "test-node-secret")
			if err != nil || desired != nil || actual != supported {
				t.Fatalf("watch result supported=%v err=%v", actual, err)
			}
			if ordinary != map[bool]int{true: 0, false: 1}[supported] {
				t.Fatal("incorrect fallback")
			}
		})
	}
}
