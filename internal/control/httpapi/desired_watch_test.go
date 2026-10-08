package httpapi_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/httpapi"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type watchedRepository struct {
	*memoryrepo.Store
	read chan struct{}
}

func (s *watchedRepository) DesiredNodeConfig(ctx context.Context, id string) (generations.NodeConfigGeneration, int64, error) {
	configuration, applied, err := s.Store.DesiredNodeConfig(ctx, id)
	select {
	case s.read <- struct{}{}:
	default:
	}
	return configuration, applied, err
}

func TestDesiredWatchWakesAfterCommitAndReauthenticates(t *testing.T) {
	for _, rotate := range []bool{false, true} {
		t.Run(map[bool]string{false: "new-generation", true: "revoked-while-waiting"}[rotate], func(t *testing.T) {
			f := newBusinessFixture(t)
			repo := &watchedRepository{Store: f.store, read: make(chan struct{}, 4)}
			handler := httpapi.New(auth.NewService(f.store, audit.NewService(f.store), time.Now, time.Hour), enrollment.NewService(f.store, time.Now, time.Hour), nodes.NewService(f.store, time.Now, time.Minute), groups.NewService(f.store, time.Now), endpoints.NewService(f.store, time.Now), generations.NewService(repo, time.Now), slog.New(slog.NewTextHandler(io.Discard, nil)))
			var issued enrollment.IssueResult
			decodeResponse(t, requestJSON(t, handler, http.MethodPost, "/api/v1/enrollment-tokens", f.token, map[string]any{"name": "watch-test", "expires_in_seconds": 900}, 201), &issued)
			var enrolled agentv1.EnrollmentResponse
			decodeResponse(t, requestJSON(t, handler, http.MethodPost, "/api/v1/agent/enroll", "", agentv1.EnrollmentRequest{EnrollmentToken: issued.Token, Hostname: "watch-test", Platform: "linux", Architecture: "amd64", AgentVersion: "test"}, 201), &enrolled)
			addMember(t, handler, f.token, f.entry.ID, enrolled.NodeID)
			request := httptest.NewRequest(http.MethodGet, "/api/v1/agent/desired-watch", nil)
			request.Header.Set("Authorization", "Bearer "+enrolled.NodeCredential)
			recorder := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { handler.ServeHTTP(recorder, request); close(done) }()
			select {
			case <-repo.read:
			case <-time.After(2 * time.Second):
				t.Fatal("watch did not begin")
			}
			if rotate {
				if _, _, err := nodes.NewService(f.store, time.Now, time.Minute).RotateCredential(context.Background(), "adm_business", enrolled.NodeID, "watch-rotate"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := generations.NewService(f.store, time.Now).CreateGroupRevision(context.Background(), "adm_business", f.entry.ID, agentv1.EngineGOST, []byte(`{"services":[]}`), "wake-generation"); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("commit did not wake watch promptly")
			}
			want := 200
			if rotate {
				want = 401
			}
			if recorder.Code != want {
				t.Fatalf("watch status=%d want=%d", recorder.Code, want)
			}
			if rotate && recorder.Body.String() != "" && containsConfig(recorder.Body.String()) {
				t.Fatal("revoked credential received configuration")
			}
		})
	}
}

func containsConfig(body string) bool {
	return strings.Contains(body, `"config"`) || strings.Contains(body, `"config_sha256"`)
}
