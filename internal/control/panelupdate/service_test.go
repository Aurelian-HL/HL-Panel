package panelupdate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/releases"
)

type releaseFixture struct{ status releases.Status }

func (f *releaseFixture) Refresh(context.Context) releases.Status { return f.status }

func TestPasswordVersionIdempotencyAndFixedWorkerRequest(t *testing.T) {
	ctx := context.Background()
	hash, _ := auth.HashPassword("isolated-password")
	repo := memoryrepo.New(auth.Administrator{ID: "admin", Username: "admin", PasswordHash: hash})
	auditor := audit.NewService(repo)
	versions := &releaseFixture{releases.Status{Versions: []releases.Version{{Tag: "v0.1.49", CanUpdate: true}}}}
	s := New(auth.NewService(repo, auditor, time.Now, time.Hour), auditor, versions)
	posts := 0
	var task *Task
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			posts++
			raw, _ := io.ReadAll(r.Body)
			if strings.Contains(string(raw), "password") || r.Header.Get("Authorization") != "" {
				t.Error("credentials reached privileged worker")
			}
			var input map[string]string
			_ = json.Unmarshal(raw, &input)
			if len(input) != 2 || input["version"] != "v0.1.49" {
				t.Error("unbounded worker input")
			}
			task = &Task{ID: input["id"], TargetVersion: input["version"], State: "running"}
			w.WriteHeader(202)
		}
		_ = json.NewEncoder(w).Encode(Status{Available: true, Task: task})
	}))
	defer worker.Close()
	u, _ := url.Parse(worker.URL)
	s.client = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", u.Host)
	}}}
	id := "71e06dd0-5081-4fdc-bd32-f3e16b50fc7d"
	for _, input := range []struct{ password, id, version string }{
		{"wrong", id, "v0.1.49"}, {"isolated-password", ";sh", "v0.1.49"}, {"isolated-password", id, "v0.1.1"}, {"isolated-password", id, "v0.1.49;sh"},
	} {
		if _, err := s.Start(ctx, "admin", input.password, input.id, input.version); !errors.Is(err, faults.ErrValidation) {
			t.Fatalf("invalid request: %v", err)
		}
	}
	if posts != 0 {
		t.Fatal("invalid request started privileged updater")
	}
	if _, err := s.Start(ctx, "admin", "isolated-password", id, "v0.1.49"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(ctx, "admin", "isolated-password", id, "v0.1.49"); err != nil || posts != 1 {
		t.Fatal("retry was not idempotent")
	}
	if _, err := s.Start(ctx, "admin", "isolated-password", id, "v0.1.50"); !errors.Is(err, faults.ErrIdempotencyConflict) {
		t.Fatal("task version can change")
	}
	if _, err := s.Start(ctx, "admin", "isolated-password", "bbcbfd80-0001-4000-8000-000000000001", "v0.1.49"); !errors.Is(err, faults.ErrConflict) {
		t.Fatal("concurrent update accepted")
	}
	worker.Close()
	if s.Status(ctx).Available {
		t.Fatal("missing worker claimed available")
	}
}
