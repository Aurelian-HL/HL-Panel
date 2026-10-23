package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type liveGOSTRunner struct {
	recordingGOSTRunner
	calls   []string
	fail    map[string]error
	objects map[string]bool
	state   string
}

func (r *liveGOSTRunner) GOSTAPI(_ context.Context, _ *gostLocalAPI, method, path string, body []byte) ([]byte, error) {
	r.calls = append(r.calls, method+" "+path)
	if err := r.fail[method+" "+path]; err != nil {
		return nil, err
	}
	switch method {
	case http.MethodPost:
		r.objects[path+"/"+gostObjectName(body)] = true
	case http.MethodDelete:
		delete(r.objects, path)
	case http.MethodGet:
		if strings.Count(path, "/") == 3 {
			if !r.objects[path] {
				return []byte(`{"data":null}`), nil
			}
			state := r.state
			if state == "" {
				state = "ready"
			}
			return json.Marshal(map[string]any{"data": map[string]any{"status": map[string]any{"state": state}}})
		}
	}
	return []byte(`{"code":0}`), nil
}
func livePrepared(t *testing.T, a *GOSTProcessAdapter, generation agentv1.NodeConfigGeneration, add bool) PreparedConfiguration {
	t.Helper()
	fragments := []agentv1.ConfigurationFragment{gostFragment("a", gostServiceA)}
	if add {
		fragments = append(fragments, gostFragment("b", strings.ReplaceAll(strings.ReplaceAll(gostServiceA, "forward-a", "forward-b"), "18081", "18082")))
	}
	p, err := a.Prepare(context.Background(), testDesired(t, generation, fragments))
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestGOSTLiveAdditionAndRollbackKeepProcess(t *testing.T) {
	r := &liveGOSTRunner{objects: map[string]bool{}}
	a := newTestGOSTAdapter(t, r, true)
	first := livePrepared(t, a, 1, false)
	if err := a.Commit(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	original, epoch := a.process, a.CounterEpoch()
	second := livePrepared(t, a, 2, true)
	if err := a.Commit(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if err := a.Verify(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if a.process != original || !original.Running() || a.CounterEpoch() != epoch || len(r.processes) != 1 {
		t.Fatal("addition replaced original process")
	}
	if !r.objects["/config/services/forward-b"] {
		t.Fatal("missing added service")
	}
	if err := a.Rollback(context.Background(), &first); err != nil {
		t.Fatal(err)
	}
	if err := a.Verify(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if a.process != original || a.CounterEpoch() != epoch || r.objects["/config/services/forward-b"] {
		t.Fatal("rollback disturbed old service or retained addition")
	}
}
func TestGOSTLiveAdditionFailureRestoresLKG(t *testing.T) {
	for _, tt := range []struct {
		name, at string
		err      error
		state    string
		blocked  bool
	}{
		{name: "rejected post", at: "POST /config/services", err: ErrGOSTLiveUpdate},
		{name: "unknown post completion", at: "POST /config/services", err: errGOSTAPIUncertain, blocked: true},
		{name: "failed readiness", state: "failed"},
		{name: "failed compensation", at: "DELETE /config/services/forward-b", err: ErrGOSTLiveUpdate, state: "failed", blocked: true},
		{name: "unknown delete completion", at: "DELETE /config/services/forward-b", err: errGOSTAPIUncertain, state: "failed", blocked: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := &liveGOSTRunner{objects: map[string]bool{}, fail: map[string]error{}, state: tt.state}
			a := newTestGOSTAdapter(t, r, true)
			first := livePrepared(t, a, 1, false)
			if err := a.Commit(context.Background(), first); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(a.activePath)
			original := a.process
			r.fail[tt.at] = tt.err
			second := livePrepared(t, a, 2, true)
			if !errors.Is(a.Commit(context.Background(), second), ErrGOSTLiveUpdate) {
				t.Fatal("failed addition reported success")
			}
			after, _ := os.ReadFile(a.activePath)
			if !bytes.Equal(before, after) || a.processConfigSHA256 != sha256.Sum256(before) {
				t.Fatal("LKG file/hash changed")
			}
			if err := a.Verify(context.Background(), first); (err != nil) != tt.blocked || tt.blocked && !errors.Is(err, ErrLiveStateUncertain) {
				t.Fatalf("verify=%v blocked=%v", err, tt.blocked)
			}
			if a.process != original || !original.Running() || r.processes[0].stopCount != 0 {
				t.Fatal("failure recovery replaced the process")
			}
		})
	}
}
func TestGOSTLiveAdditionFileFailureCompensatesWithoutRestart(t *testing.T) {
	r := &liveGOSTRunner{objects: map[string]bool{}}
	a := newTestGOSTAdapter(t, r, true)
	first := livePrepared(t, a, 1, false)
	if err := a.Commit(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	previous, _ := os.ReadFile(a.activePath)
	second := livePrepared(t, a, 2, true)
	candidate, _ := os.ReadFile(second.adapterPath)
	original, path := a.process, a.activePath
	blocked := filepath.Join(t.TempDir(), "nonempty-directory")
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "keep"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.activePath = blocked
	applied, err := a.tryAddGOSTLiveLocked(context.Background(), previous, candidate)
	a.activePath = path
	if !applied || !errors.Is(err, ErrGOSTLiveUpdate) || a.process != original || !original.Running() || r.objects["/config/services/forward-b"] {
		t.Fatal("file failure failed to compensate live additions")
	}
	if err := a.Verify(context.Background(), first); !errors.Is(err, ErrLiveStateUncertain) {
		t.Fatalf("failed file recovery falsely attested: %v", err)
	}
}

func TestGOSTLiveAdditionMixedTransactionRollbackKeepsProcess(t *testing.T) {
	r := &liveGOSTRunner{objects: map[string]bool{}}
	g := newTestGOSTAdapter(t, r, true)
	x := newTestXrayAdapter(t, &recordingXrayRunner{}, true)
	a, err := NewMixedProcessAdapter(x, g)
	if err != nil {
		t.Fatal(err)
	}
	fragments := mixedFragments("xray-one", "forward-one")
	first, err := a.Prepare(context.Background(), testDesired(t, 1, fragments))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Commit(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	gp, xp, epoch := g.process, x.process, g.CounterEpoch()
	fragments = append(fragments, gostFragment("second", strings.ReplaceAll(strings.ReplaceAll(gostServiceA, "forward-a", "forward-b"), "18081", "18082")))
	second, err := a.Prepare(context.Background(), testDesired(t, 2, fragments))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Commit(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	// The reconciler requests complete rollback after a later journal failure.
	if err := a.Rollback(context.Background(), &first); err != nil {
		t.Fatal(err)
	}
	if err := a.Verify(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if g.process != gp || x.process != xp || g.CounterEpoch() != epoch || r.objects["/config/services/forward-b"] {
		t.Fatal("transaction rollback disturbed existing engines")
	}
}

func TestGOSTAdditionPlannerRejectsChangesAndRemovals(t *testing.T) {
	before := []byte(`{"services":[{"name":"a","addr":":123"}],"api":{"addr":"127.0.0.1:9"}}`)
	for _, after := range []string{
		`{"services":[{"name":"a","addr":":456"},{"name":"b"}],"api":{"addr":"127.0.0.1:9"}}`,
		`{"services":[{"name":"b"}],"api":{"addr":"127.0.0.1:9"}}`,
		`{"services":[{"name":"a","addr":":123"},{"name":"a"}],"api":{"addr":"127.0.0.1:9"}}`,
		`{"services":[{"name":"a","addr":":123"},{"name":"b"}],"api":{"addr":"0.0.0.0:9"}}`,
	} {
		if _, ok := planGOSTAdditions(before, []byte(after)); ok {
			t.Fatal("unsafe delta admitted")
		}
	}
	after := []byte(`{"api":{"addr":"127.0.0.1:9"},"services":[{"addr":":123","name":"a"},{"name":"b"}],"chains":[{"name":"new-chain"}]}`)
	delta, ok := planGOSTAdditions(before, after)
	if !ok || len(delta.services) != 1 || len(delta.chains) != 1 {
		t.Fatal("pure addition not recognized")
	}
}
func TestGOSTAPIIsLoopbackAuthenticatedAndBounded(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		user, pass, ok := req.BasicAuth()
		if !ok || user != "hl-agent" || pass != "disposable-secret" {
			t.Error("missing authentication")
		}
		if req.URL.Path == "/config/chains" {
			http.Redirect(w, req, "http://127.0.0.1:1/secret", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer server.Close()
	api := &gostLocalAPI{Addr: strings.TrimPrefix(server.URL, "http://")}
	api.Auth.Username = "hl-agent"
	api.Auth.Password = "disposable-secret"
	r := &ExecGOSTRunner{}
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	if _, err := r.GOSTAPI(context.Background(), api, http.MethodGet, "/config/services", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.GOSTAPI(context.Background(), api, http.MethodGet, "/config/chains", nil); err == nil {
		t.Fatal("redirect accepted")
	}
	for _, path := range []string{"/config/services/a/../../config", "/config/services/..", "/config/services/a?x=1", "//evil/config"} {
		if _, err := r.GOSTAPI(context.Background(), api, http.MethodDelete, path, nil); err == nil {
			t.Fatal("unsafe path accepted")
		}
	}
	api.Addr = "0.0.0.0:1234"
	if _, err := r.GOSTAPI(context.Background(), api, http.MethodGet, "/config/services", nil); err == nil {
		t.Fatal("non-loopback accepted")
	}
	if calls != 2 {
		t.Fatalf("unexpected HTTP calls %d", calls)
	}
}
