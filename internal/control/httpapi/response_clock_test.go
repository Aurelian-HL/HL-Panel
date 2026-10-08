package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/faults"
)

func TestProblemDistinguishesOutageAndExpiredAuthentication(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
	}{{errors.New("database unavailable"), http.StatusInternalServerError}, {faults.ErrUnauthorized, http.StatusUnauthorized}} {
		writer := httptest.NewRecorder()
		writeProblem(writer, httptest.NewRequest(http.MethodGet, "/api/v1/device-groups", nil), test.err)
		if writer.Code != test.status {
			t.Fatalf("status = %d, want %d", writer.Code, test.status)
		}
		if writer.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("authenticated response may be cached")
		}
		if _, err := time.Parse(time.RFC3339Nano, writer.Header().Get("X-HL-Server-Time")); err != nil {
			t.Fatalf("missing server clock: %v", err)
		}
	}
}
