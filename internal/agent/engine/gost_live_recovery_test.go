package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type gostRecoveryRunner struct {
	liveGOSTRunner
	fault                           string
	uncertain, badUndo, unavailable bool
	after                           func()
}

func (r *gostRecoveryRunner) GOSTAPI(ctx context.Context, api *gostLocalAPI, method, path string, body []byte) ([]byte, error) {
	if r.unavailable || r.badUndo && r.fault == "" && method == http.MethodGet && strings.Count(path, "/") == 3 {
		return nil, ErrGOSTLiveUpdate
	}
	raw, err := r.liveGOSTRunner.GOSTAPI(ctx, api, method, path, body)
	if err == nil && method == http.MethodPost && r.after != nil {
		r.after()
	}
	if err == nil && r.fault == method+" "+path {
		r.fault = ""
		if r.uncertain {
			return nil, errGOSTAPIUncertain
		}
		return nil, ErrGOSTLiveUpdate
	}
	return raw, err
}
func TestGOSTLiveChangeRecoveryBoundaries(t *testing.T) {
	for _, stage := range []string{"confirmed", "uncertain", "compensation", "disk", "rollback", "unavailable"} {
		t.Run(stage, func(t *testing.T) {
			r := &gostRecoveryRunner{liveGOSTRunner: liveGOSTRunner{objects: map[string]bool{}}}
			a := newTestGOSTAdapter(t, r, true)
			first := livePrepared(t, a, 1, true)
			if err := a.Commit(context.Background(), first); err != nil {
				t.Fatal(err)
			}
			raw, _ := os.ReadFile(first.adapterPath)
			var doc map[string]json.RawMessage
			_ = json.Unmarshal(raw, &doc)
			for _, key := range []string{"services", "chains"} {
				var items []json.RawMessage
				_ = json.Unmarshal(doc[key], &items)
				for _, v := range items {
					r.objects["/config/"+key+"/"+gostObjectName(v)] = true
				}
			}
			fragment := strings.ReplaceAll(gostServiceA, "18081", "18083")
			next, err := a.Prepare(context.Background(), testDesired(t, 2, []agentv1.ConfigurationFragment{gostFragment("a", fragment), gostFragment("b", strings.ReplaceAll(strings.ReplaceAll(gostServiceA, "forward-a", "forward-b"), "18081", "18082"))}))
			if err != nil {
				t.Fatal(err)
			}
			original := a.activePath
			r.fault = "POST /config/services"
			r.uncertain = stage == "uncertain"
			r.badUndo = stage == "compensation"
			if stage == "unavailable" {
				r.unavailable = true
			}
			if stage == "disk" {
				r.fault = ""
				r.after = func() { a.activePath = filepath.Join(original, "unwritable") }
			}
			if stage == "rollback" {
				r.fault = ""
				if err := a.Commit(context.Background(), next); err != nil {
					t.Fatal(err)
				}
				r.fault = "POST /config/services"
				if err := a.Rollback(context.Background(), &first); !errors.Is(err, ErrGOSTLiveUpdate) {
					t.Fatalf("failed rollback was attested: %v", err)
				}
			} else if err := a.Commit(context.Background(), next); !errors.Is(err, ErrGOSTLiveUpdate) {
				t.Fatalf("error=%v", err)
			}
			a.activePath = original
			if stage == "uncertain" || stage == "compensation" || stage == "disk" {
				for _, op := range []func() error{
					func() error { return a.Verify(context.Background(), first) },
					func() error { return a.Commit(context.Background(), next) },
					func() error { return a.Rollback(context.Background(), &first) },
				} {
					if err := op(); !errors.Is(err, ErrLiveStateUncertain) {
						t.Fatalf("uncertain state accepted: %v", err)
					}
				}
			} else {
				verified := first
				if stage == "rollback" {
					verified = next
				}
				if err := a.Verify(context.Background(), verified); err != nil {
					t.Fatal(err)
				}
			}
			if len(r.processes) != 1 || r.processes[0].stopCount != 0 {
				t.Fatal("failure recovery restarted the process")
			}
			if !r.objects["/config/services/forward-b"] {
				t.Fatal("unrelated service removed")
			}
		})
	}
}
