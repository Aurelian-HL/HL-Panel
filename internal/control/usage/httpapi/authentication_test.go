package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hongle/hl-panel/internal/control/faults"
)

func TestAuthenticationFailuresDoNotRevokeValidIdentitiesOrReachServices(t *testing.T) {
	routes := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/usage/reports"},
		{http.MethodGet, "/api/v1/usage"},
		{http.MethodGet, "/api/v1/usage/enforcement/desired"},
		{http.MethodPost, "/api/v1/usage/enforcement/results"},
		{http.MethodPost, "/api/v1/usage/enforcement/decision/revoke"},
	}
	cases := []struct {
		name   string
		err    error
		id     string
		status int
		code   string
	}{
		{"database outage", errors.New("private database DSN unavailable"), "", 503, "service_unavailable"},
		{"wrapped outage", fmt.Errorf("query: %w", faults.ErrUnavailable), "", 503, "service_unavailable"},
		{"invalid credential", fmt.Errorf("revoked: %w", faults.ErrUnauthorized), "", 401, "unauthorized"},
		{"missing identity", faults.ErrNotFound, "", 401, "unauthorized"},
		{"expired", faults.ErrExpired, "", 401, "unauthorized"},
		{"empty identity", nil, "", 401, "unauthorized"},
		{"password change", faults.ErrPasswordChangeRequired, "", 403, "password_change_required"},
		{"error with identity", faults.ErrUnavailable, "untrusted-id", 503, "service_unavailable"},
	}
	for _, route := range routes {
		for _, test := range cases {
			t.Run(route.path+"/"+test.name, func(t *testing.T) {
				service := &stubService{}
				authenticate := func(*http.Request) (string, error) { return test.id, test.err }
				handler, err := New(service, authenticate, authenticate)
				if err != nil {
					t.Fatal(err)
				}
				mux := http.NewServeMux()
				if err = handler.Register(mux); err != nil {
					t.Fatal(err)
				}
				response := httptest.NewRecorder()
				mux.ServeHTTP(response, httptest.NewRequest(route.method, route.path, strings.NewReader("{}")))
				if response.Code != test.status || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) || service.calls != 0 {
					t.Fatalf("status=%d calls=%d body=%s", response.Code, service.calls, response.Body.String())
				}
				if strings.Contains(response.Body.String(), "private database") || strings.Contains(response.Body.String(), "DSN") {
					t.Fatal("authentication leaked infrastructure details")
				}
				challenge := response.Header().Get("WWW-Authenticate")
				if (test.status == 401) != (challenge == "Bearer") {
					t.Fatalf("unexpected authentication challenge: %q", challenge)
				}
				if test.status == 503 && response.Header().Get("Retry-After") != "15" {
					t.Fatal("transient outage did not provide retry guidance")
				}
			})
		}
	}
}
