package controlclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func testApplyPair() []agentv1.ApplyResultRequest {
	a := agentv1.ApplyResultRequest{Generation: 1, AttemptID: strings.Repeat("a", 32), ConfigSHA256: strings.Repeat("b", 64), Phase: agentv1.ApplyPhaseCommit, Status: agentv1.ApplyStatusSucceeded, EngineMode: "xray"}
	b := a
	b.Phase = agentv1.ApplyPhaseVerify
	return []agentv1.ApplyResultRequest{a, b}
}

func TestApplyBatchWireAndLegacyFallback(t *testing.T) {
	for _, status := range []int{204, 404, 405, 501, 401, 403, 409, 500, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			pair := testApplyPair()
			batchCalls, singleCalls := 0, 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assertBearer(t, r, "node-secret")
				if r.Method != http.MethodPost {
					t.Error("wrong method")
				}
				if r.URL.Path == "/api/v1/agent/apply-results/batch" {
					batchCalls++
					var body agentv1.ApplyResultsRequest
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !reflect.DeepEqual(body.Results, pair) {
						t.Error("wrong batch body", err)
					}
					w.WriteHeader(status)
					return
				}
				if r.URL.Path != "/api/v1/agent/apply-results" {
					t.Error("wrong fallback path")
				}
				singleCalls++
				w.WriteHeader(204)
			}))
			defer server.Close()
			client, err := New(server.URL, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			fallback := status == 404 || status == 405 || status == 501
			for i := 0; i < 2; i++ {
				err = client.ReportApplyResults(context.Background(), "node-secret", pair)
				if (err == nil) != (status == 204 || fallback) {
					t.Fatal("unexpected result", err)
				}
			}
			wantBatch, wantSingle := 2, 0
			if fallback {
				wantBatch, wantSingle = 1, 4
			}
			if batchCalls != wantBatch || singleCalls != wantSingle {
				t.Fatalf("batch=%d single=%d", batchCalls, singleCalls)
			}
		})
	}
}

func TestApplyBatchPartialLegacyFailureIsRetried(t *testing.T) {
	var phases []agentv1.ApplyPhase
	fail := true
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/agent/apply-results/batch" {
			w.WriteHeader(404)
			return
		}
		var body agentv1.ApplyResultRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		phases = append(phases, body.Phase)
		if body.Phase == agentv1.ApplyPhaseVerify && fail {
			fail = false
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	client, _ := New(server.URL, server.Client())
	if err := client.ReportApplyResults(context.Background(), "node-secret", testApplyPair()); err == nil {
		t.Fatal("partial delivery acknowledged")
	}
	if err := client.ReportApplyResults(context.Background(), "node-secret", testApplyPair()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(phases, []agentv1.ApplyPhase{"commit", "verify", "commit", "verify"}) {
		t.Fatal(phases)
	}
}

func TestApplyBatchTransportFailureDoesNotFallback(t *testing.T) {
	called := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agent/apply-results/batch" {
			t.Error("transport failure triggered fallback")
		}
		called <- struct{}{}
		<-release
	}))
	defer server.Close()
	defer close(release)
	client, _ := New(server.URL, server.Client())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.ReportApplyResults(ctx, "node-secret", testApplyPair()) }()
	<-called
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("unexpected error", err)
	}
	if client.applyBatchUnsupported.Load() {
		t.Fatal("transport failure disabled batching")
	}
}
