package controlclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
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
					if r.URL.Query().Get("applied_generation") != "7" {
						t.Errorf("applied_generation = %q, want 7", r.URL.Query().Get("applied_generation"))
					}
					if got, _ := strconv.Atoi(r.URL.Query().Get("wait_ms")); got < 750 || got > 1000 {
						t.Errorf("watch wait = %dms, want jittered wait between 750ms and 1000ms", got)
					}
					if !supported {
						w.WriteHeader(404)
						return
					}
					w.Header().Set("X-Desired-Watch-Waited", "true")
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
			desired, actualSupported, waited, err := client.DesiredWatch(context.Background(), "test-node-secret", 7)
			if err != nil || desired != nil || actualSupported != supported || waited != supported {
				t.Fatalf("watch result supported=%v waited=%v err=%v", actualSupported, waited, err)
			}
			if ordinary != map[bool]int{true: 0, false: 1}[supported] {
				t.Fatal("incorrect fallback")
			}
		})
	}
}

func TestDesiredWatchDefaultWaitFitsAgentRequestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, _ := strconv.Atoi(r.URL.Query().Get("wait_ms")); got < 6000 || got > 8000 {
			t.Errorf("default watch wait = %dms, want jittered wait between 6000ms and 8000ms", got)
		}
		w.Header().Set("X-Desired-Watch-Waited", "true")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	httpClient := server.Client()
	httpClient.Timeout = 15 * time.Second
	client, err := NewWithOptions(server.URL, httpClient, true)
	if err != nil {
		t.Fatal(err)
	}
	_, supported, waited, err := client.DesiredWatch(context.Background(), "test-node-secret", 7)
	if err != nil || !supported || !waited {
		t.Fatalf("watch supported=%v waited=%v err=%v", supported, waited, err)
	}
}

func TestDesiredWatchRetriesPreviousPanelWaitLimit(t *testing.T) {
	var waits []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wait, _ := strconv.Atoi(r.URL.Query().Get("wait_ms"))
		waits = append(waits, wait)
		if r.URL.Path != "/api/v1/agent/desired-watch" || r.URL.Query().Get("applied_generation") != "7" || r.Header.Get("Authorization") != "Bearer test-node-secret" {
			t.Error("compatibility retry changed endpoint, generation, or authentication")
		}
		if wait > 2000 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"generation":8,"engine":"node-bundle","config_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","config":{"schema_version":1,"fragments":[]}}`))
	}))
	defer server.Close()
	httpClient := server.Client()
	httpClient.Timeout = 15 * time.Second
	client, err := NewWithOptions(server.URL, httpClient, true)
	if err != nil {
		t.Fatal(err)
	}
	desired, supported, waited, err := client.DesiredWatch(context.Background(), "test-node-secret", 7)
	if err != nil || desired == nil || desired.Generation != 8 || !supported || waited {
		t.Fatalf("legacy wait retry desired=%v supported=%v waited=%v err=%v", desired, supported, waited, err)
	}
	if len(waits) != 2 || waits[0] <= 2000 || waits[1] != 2000 {
		t.Fatalf("watch waits = %v, want initial long wait and one bounded retry", waits)
	}
}

func TestDesiredWatchFitsShorterContextDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, _ := strconv.Atoi(r.URL.Query().Get("wait_ms")); got < 1450 || got > 2000 {
			t.Errorf("watch wait = %dms, want jittered wait within half of the 4s context deadline", got)
		}
		w.Header().Set("X-Desired-Watch-Waited", "true")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	httpClient := server.Client()
	httpClient.Timeout = 15 * time.Second
	client, err := NewWithOptions(server.URL, httpClient, true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	_, supported, waited, err := client.DesiredWatch(ctx, "test-node-secret", 7)
	if err != nil || !supported || !waited {
		t.Fatalf("watch supported=%v waited=%v err=%v", supported, waited, err)
	}
}
