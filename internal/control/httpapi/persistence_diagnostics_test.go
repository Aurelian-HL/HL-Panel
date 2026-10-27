package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hongle/hl-panel/internal/control/faults"
)

type isolatedPersistenceFailure struct{}

func (isolatedPersistenceFailure) Error() string { return "raw-isolated-private-cause" }
func (isolatedPersistenceFailure) SafePersistenceDiagnostic() string {
	return "stage=snapshot_lock category=deadline sql_state=57014 outcome=not_committed"
}

func TestPersistenceProblemLogsSafeStageWithoutRequestSecrets(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	api := &API{logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/forwarding-rules/{id}", func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, fmt.Errorf("private-outer-message: %w", isolatedPersistenceFailure{}))
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/forwarding-rules/private-rule-id?token=private-query-token", strings.NewReader("private-request-body"))
	request.Header.Set("Authorization", "Bearer private-authorization")
	request.Header.Set("Idempotency-Key", "private-idempotency-key")
	request.Header.Set("X-Request-ID", "private-client-request-id")
	writer := httptest.NewRecorder()
	api.securityHeaders(mux).ServeHTTP(writer, request)
	if writer.Code != http.StatusInternalServerError || !strings.Contains(writer.Body.String(), "internal_error") {
		t.Fatal("persistence failure changed HTTP contract", writer.Code)
	}
	if !strings.Contains(logs.String(), "stage=snapshot_lock") || !strings.Contains(logs.String(), "outcome=not_committed") ||
		!strings.Contains(logs.String(), "POST /api/v1/forwarding-rules/{id}") || !strings.Contains(logs.String(), "elapsed_ms") {
		t.Fatal("diagnostic stage, route or duration missing", logs.String())
	}
	var diagnostic map[string]any
	if err := json.Unmarshal(logs.Bytes(), &diagnostic); err != nil {
		t.Fatal(err)
	}
	id := writer.Header().Get("X-Request-ID")
	if len(id) < 20 || diagnostic["request_id"] != id {
		t.Fatal("response and failure log do not share a generated request ID")
	}
	for _, secret := range []string{"private-rule-id", "private-query-token", "private-request-body", "private-authorization",
		"private-idempotency-key", "private-client-request-id", "raw-isolated-private-cause", "private-outer-message"} {
		if strings.Contains(logs.String(), secret) || strings.Contains(writer.Body.String(), secret) {
			t.Fatal("HTTP failure exposed request or database secrets")
		}
	}
	logs.Reset()
	request = withRequestDiagnostics(httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil), logger)
	writer = httptest.NewRecorder()
	writeProblem(writer, request, faults.ErrUnauthorized)
	if writer.Code != http.StatusUnauthorized || logs.Len() != 0 {
		t.Fatal("expected authentication rejection was logged as a persistence outage")
	}
}
