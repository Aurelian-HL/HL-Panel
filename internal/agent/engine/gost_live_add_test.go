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

type gostAdditionAbsenceRunner struct {
	liveGOSTRunner
	response        []byte
	deleted, absent bool
}

func (r *gostAdditionAbsenceRunner) GOSTAPI(ctx context.Context, api *gostLocalAPI, method, path string, body []byte) ([]byte, error) {
	if path == "/config/services/forward-b" {
		if method == http.MethodDelete {
			r.deleted = true
			if r.absent {
				delete(r.objects, path)
			}
		}
		if method == http.MethodGet && r.deleted {
			r.calls = append(r.calls, method+" "+path)
			return r.response, nil
		}
	}
	return r.liveGOSTRunner.GOSTAPI(ctx, api, method, path, body)
}

func TestGOSTAdditionCompensationRequiresExplicitAbsence(t *testing.T) {
	for _, tt := range []struct {
		name, response string
		absent         bool
	}{
		{name: "missing data", response: `{}`},
		{name: "success without data", response: `{"code":0}`},
		{name: "null response", response: `null`},
		{name: "malformed response", response: `{"data":`},
		{name: "service still present", response: `{"data":{}}`},
		{name: "confirmed absence", response: `{"data":null}`, absent: true},
		{name: "formatted absence", response: "{\"data\": \n null }", absent: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := &gostAdditionAbsenceRunner{
				liveGOSTRunner: liveGOSTRunner{objects: map[string]bool{}, fail: map[string]error{}, state: "failed"},
				response:       []byte(tt.response), absent: tt.absent,
			}
			a := newTestGOSTAdapter(t, r, true)
			first := livePrepared(t, a, 1, false)
			if err := a.Commit(context.Background(), first); err != nil {
				t.Fatal(err)
			}
			original, epoch := a.process, a.CounterEpoch()
			before, err := os.ReadFile(a.activePath)
			if err != nil {
				t.Fatal(err)
			}
			r.fail["DELETE /config/services/forward-b"] = ErrGOSTLiveUpdate
			second := livePrepared(t, a, 2, true)
			err = a.Commit(context.Background(), second)
			if !errors.Is(err, ErrGOSTLiveUpdate) || errors.Is(err, ErrLiveStateUncertain) != !tt.absent {
				t.Fatalf("commit=%v; explicitly absent=%v", err, tt.absent)
			}
			if err := a.Verify(context.Background(), first); (err == nil) != tt.absent || !tt.absent && !errors.Is(err, ErrLiveStateUncertain) {
				t.Fatalf("verify=%v; explicitly absent=%v", err, tt.absent)
			}
			if !tt.absent {
				calls := len(r.calls)
				if err := a.Commit(context.Background(), second); !errors.Is(err, ErrLiveStateUncertain) {
					t.Fatalf("uncertain state allowed another commit: %v", err)
				}
				if err := a.Rollback(context.Background(), &first); !errors.Is(err, ErrLiveStateUncertain) {
					t.Fatalf("uncertain state allowed rollback: %v", err)
				}
				if len(r.calls) != calls || !r.objects["/config/services/forward-b"] {
					t.Fatal("uncertain state performed further mutations or lost retained candidate")
				}
			}
			after, err := os.ReadFile(a.activePath)
			if err != nil || !bytes.Equal(before, after) || a.processConfigSHA256 != sha256.Sum256(before) {
				t.Fatalf("changed last-known-good file/hash: %v", err)
			}
			if a.process != original || !original.Running() || a.CounterEpoch() != epoch || len(r.processes) != 1 || r.processes[0].stopCount != 0 {
				t.Fatal("absence verification stopped or replaced the running core")
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

func TestGOSTAPIMutationRequiresExplicitSuccess(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		for _, test := range []struct {
			name string
			body string
			want error
		}{
			{"success", `{"code":0}`, nil},
			{"gost success", `{"code":null,"msg":"OK"}`, nil},
			{"message success", `{"msg":"OK"}`, nil},
			{"rejected", `{"code":40001}`, ErrGOSTLiveUpdate},
			{"rejection wins over message", `{"code":40001,"msg":"OK"}`, ErrGOSTLiveUpdate},
			{"missing code", `{}`, errGOSTAPIUncertain},
			{"null envelope", `null`, errGOSTAPIUncertain},
			{"null code", `{"code":null}`, errGOSTAPIUncertain},
			{"data without code", `{"data":null}`, errGOSTAPIUncertain},
			{"unknown message", `{"msg":"pending"}`, errGOSTAPIUncertain},
			{"wrong code type", `{"code":"0"}`, errGOSTAPIUncertain},
			{"malformed", `{`, errGOSTAPIUncertain},
		} {
			t.Run(method+"/"+test.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					_, _ = w.Write([]byte(test.body))
				}))
				defer server.Close()
				api := &gostLocalAPI{Addr: strings.TrimPrefix(server.URL, "http://")}
				path := "/config/services"
				if method == http.MethodDelete {
					path += "/test-service"
				}
				_, err := (&ExecGOSTRunner{}).GOSTAPI(context.Background(), api, method, path, nil)
				if !errors.Is(err, test.want) {
					t.Fatalf("error = %v, want %v", err, test.want)
				}
			})
		}
	}
}

func TestGOSTAPIReadAllowsDataWithoutCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = w.Write([]byte(`{"data":null}`))
	}))
	defer server.Close()
	api := &gostLocalAPI{Addr: strings.TrimPrefix(server.URL, "http://")}
	if _, err := (&ExecGOSTRunner{}).GOSTAPI(context.Background(), api, http.MethodGet, "/config/services/test-service", nil); err != nil {
		t.Fatal(err)
	}
}

type gostAcknowledgementRunner struct {
	liveGOSTRunner
	responseAPI *gostLocalAPI
}

func (r *gostAcknowledgementRunner) GOSTAPI(ctx context.Context, api *gostLocalAPI, method, path string, body []byte) ([]byte, error) {
	raw, err := r.liveGOSTRunner.GOSTAPI(ctx, api, method, path, body)
	if err == nil && method != http.MethodGet {
		// Model a completed mutation whose HTTP response has no acknowledgement.
		return (&ExecGOSTRunner{}).GOSTAPI(ctx, r.responseAPI, method, path, body)
	}
	return raw, err
}

func TestGOSTUnacknowledgedMutationDoesNotAdvanceAppliedState(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		for _, response := range []string{`{}`, `null`, `{"code":null}`, `{"data":null}`} {
			t.Run(method+"/"+response, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					_, _ = w.Write([]byte(response))
				}))
				defer server.Close()
				r := &gostAcknowledgementRunner{
					liveGOSTRunner: liveGOSTRunner{objects: map[string]bool{}},
					responseAPI:    &gostLocalAPI{Addr: strings.TrimPrefix(server.URL, "http://")},
				}
				a := newTestGOSTAdapter(t, r, true)
				initial := livePrepared(t, a, 1, method == http.MethodDelete)
				if err := a.Commit(context.Background(), initial); err != nil {
					t.Fatal(err)
				}
				before, err := os.ReadFile(a.activePath)
				if err != nil {
					t.Fatal(err)
				}
				var config struct{ Services []json.RawMessage }
				if err := json.Unmarshal(before, &config); err != nil {
					t.Fatal(err)
				}
				for _, service := range config.Services {
					r.objects["/config/services/"+gostObjectName(service)] = true
				}
				next := livePrepared(t, a, 2, method == http.MethodPost)
				if err := a.Commit(context.Background(), next); !errors.Is(err, ErrLiveStateUncertain) {
					t.Fatalf("unacknowledged mutation accepted: %v", err)
				}
				after, err := os.ReadFile(a.activePath)
				if err != nil || !bytes.Equal(before, after) || a.processConfigSHA256 != sha256.Sum256(before) {
					t.Fatal("unacknowledged configuration advanced applied state")
				}
				if !r.objects["/config/services/forward-a"] || r.objects["/config/services/forward-b"] != (method == http.MethodPost) {
					t.Fatal("fault did not model the completed target mutation with unrelated service preserved")
				}
				calls := len(r.calls)
				for _, op := range []func() error{
					func() error { return a.Commit(context.Background(), next) },
					func() error { return a.Rollback(context.Background(), &initial) },
					func() error { return a.Verify(context.Background(), initial) },
				} {
					if err := op(); !errors.Is(err, ErrLiveStateUncertain) {
						t.Fatalf("uncertain state accepted: %v", err)
					}
				}
				if len(r.calls) != calls || len(r.processes) != 1 || r.processes[0].stopCount != 0 {
					t.Fatal("uncertain completion caused more mutations or a restart")
				}
			})
		}
	}
}
