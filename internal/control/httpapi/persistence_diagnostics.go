package httpapi

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

type requestDiagnosticsKey struct{}

type requestDiagnostics struct {
	logger  *slog.Logger
	started time.Time
	id      string
}

func withRequestDiagnostics(request *http.Request, logger *slog.Logger) *http.Request {
	// Generate locally. Client headers and idempotency keys can contain secrets
	// or attacker-controlled log content and must never become correlation IDs.
	return request.WithContext(context.WithValue(request.Context(), requestDiagnosticsKey{}, requestDiagnostics{logger: logger, started: time.Now(), id: rand.Text()}))
}

func logPersistenceFailure(request *http.Request, err error) {
	var diagnostic interface{ SafePersistenceDiagnostic() string }
	if !errors.As(err, &diagnostic) {
		return
	}
	entry, ok := request.Context().Value(requestDiagnosticsKey{}).(requestDiagnostics)
	if !ok || entry.logger == nil {
		return
	}
	// The route is a ServeMux pattern with placeholders, never a raw URL, query,
	// request body or idempotency key. Do not log err or its unwrapped cause.
	entry.logger.WarnContext(request.Context(), "持久化请求失败："+diagnostic.SafePersistenceDiagnostic()+" route="+request.Pattern,
		"request_id", entry.id, "elapsed_ms", time.Since(entry.started).Milliseconds())
}
