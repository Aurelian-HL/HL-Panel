package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteGuardedUnavailableUsesStructuredRetryableProblem(t *testing.T) {
	for _, test := range []struct {
		name, code, message, retryAfter string
	}{
		{name: "migration", code: "migration_isolated", message: "面板正在迁移，业务已隔离", retryAfter: "15"},
		{name: "recovery", code: "panel_recovering", message: "面板正在恢复，请稍后重新登录", retryAfter: "2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/v1/device-groups", nil)
			response := httptest.NewRecorder()

			writeGuardedUnavailable(response, test.code, test.message, test.retryAfter)

			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
			}
			if response.Header().Get("Retry-After") != test.retryAfter {
				t.Fatalf("Retry-After = %q, want %s", response.Header().Get("Retry-After"), test.retryAfter)
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", response.Header().Get("Cache-Control"))
			}
			if got := response.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
				t.Fatalf("Content-Type = %q, want application/json; charset=utf-8", got)
			}
			var body struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Error.Code != test.code || body.Error.Message != test.message {
				t.Fatalf("problem = %#v, want %s problem", body.Error, test.code)
			}
			_ = request
		})
	}
}
