package panellicense

import (
	"encoding/json"
	"net/http"
)

// Guard restricts business writes while leaving authentication and all existing
// quota/authorization checks to the original handlers.
func Guard(client *Client, next http.Handler) http.Handler {
	if client == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !client.Status("").ManagementAllowed && !OperationAllowed(r) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "license_management_restricted", "message": "面板授权暂不可用，新增、修改和删除已受限；现有转发及订阅继续运行，请同步或续期授权"}})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// OperationAllowed preserves existing traffic and authenticated operational
// reports. Authorization and business quota checks still run in each handler.
func OperationAllowed(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return true
	}
	if r.Method == http.MethodPut && (r.URL.Path == "/api/v1/auth/password" || r.URL.Path == "/api/v1/customer/password") {
		return true
	}
	if r.Method != http.MethodPost {
		return false
	}
	switch r.URL.Path {
	case "/api/v1/auth/login", "/api/v1/customer/auth/login", "/api/v1/customer/auth/logout",
		"/api/v1/license/sync", "/api/v1/license/activation/prepare", "/api/v1/license/activation/commit", "/api/v1/license/reminders/dismiss",
		"/api/v1/agent/heartbeat", "/api/v1/agent/control-results", "/api/v1/agent/apply-results", "/api/v1/agent/update-task/result",
		"/api/v1/usage/reports", "/api/v1/usage/enforcement/results", "/api/v1/gateway/membership":
		return true
	}
	return false
}
