package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func liveAddFragment(extra bool) json.RawMessage {
	in := []any{map[string]any{"tag": "old-in", "protocol": "socks", "port": 18080}}
	out := []any{map[string]any{"tag": "old-out", "protocol": "freedom"}}
	rules := []any{map[string]any{"type": "field", "inboundTag": []string{"old-in"}, "outboundTag": "old-out"}}
	if extra {
		in = append(in, map[string]any{"tag": "new-in", "protocol": "socks", "port": 18081})
		out = append(out, map[string]any{"tag": "new-out", "protocol": "blackhole"})
		rules = append(rules, map[string]any{"type": "field", "inboundTag": []string{"new-in"}, "outboundTag": "new-out"})
	}
	data, _ := json.Marshal(map[string]any{"inbounds": in, "outbounds": out, "routing": map[string]any{"rules": rules}})
	return data
}

func liveAddPrepared(t *testing.T, a *XrayProcessAdapter, generation agentv1.NodeConfigGeneration, extra bool) PreparedConfiguration {
	t.Helper()
	p, err := a.Prepare(context.Background(), xrayDesired(t, generation, []agentv1.ConfigurationFragment{{
		GroupID: "rule", GroupRevision: agentv1.GroupRevision(generation), Engine: agentv1.EngineXray, Config: liveAddFragment(extra),
	}}))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestXrayAdditionsRejectChangesToExistingTraffic(t *testing.T) {
	a := newTestXrayAdapter(t, &recordingXrayRunner{}, false)
	old, next := liveAddPrepared(t, a, 1, false), liveAddPrepared(t, a, 2, true)
	before, _ := os.ReadFile(old.adapterPath)
	after, _ := os.ReadFile(next.adapterPath)
	if _, ok := planXrayAdditions(before, after); !ok {
		t.Fatal("disjoint addition rejected")
	}
	cases := map[string]func(map[string]any){
		"edit inbound":     func(m map[string]any) { m["inbounds"].([]any)[0].(map[string]any)["port"] = 19000 },
		"remove inbound":   func(m map[string]any) { m["inbounds"] = m["inbounds"].([]any)[1:] },
		"edit outbound":    func(m map[string]any) { m["outbounds"].([]any)[0].(map[string]any)["protocol"] = "blackhole" },
		"default outbound": func(m map[string]any) { v := m["outbounds"].([]any); v[0], v[1] = v[1], v[0] },
		"duplicate tag":    func(m map[string]any) { m["inbounds"].([]any)[1].(map[string]any)["tag"] = "old-in" },
		"catch all route": func(m map[string]any) {
			delete(m["routing"].(map[string]any)["rules"].([]any)[2].(map[string]any), "inboundTag")
		},
		"route old listener": func(m map[string]any) {
			m["routing"].(map[string]any)["rules"].([]any)[2].(map[string]any)["inboundTag"] = []string{"new-in", "old-in"}
		},
		"reorder old routes": func(m map[string]any) { v := m["routing"].(map[string]any)["rules"].([]any); v[0], v[1] = v[1], v[0] },
		"global policy":      func(m map[string]any) { m["policy"] = map[string]any{"system": map[string]any{}} },
		"routing settings":   func(m map[string]any) { m["routing"].(map[string]any)["domainStrategy"] = "IPIfNonMatch" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			var doc map[string]any
			_ = json.Unmarshal(after, &doc)
			change(doc)
			data, _ := json.Marshal(doc)
			if _, ok := planXrayAdditions(before, data); ok {
				t.Fatal("unsafe update accepted")
			}
		})
	}
	var doc map[string]any
	_ = json.Unmarshal(before, &doc)
	doc["api"].(map[string]any)["services"] = []string{"StatsService"}
	legacy, _ := json.Marshal(doc)
	if _, ok := planXrayAdditions(legacy, after); ok {
		t.Fatal("legacy process lacks required API")
	}
}

type liveAPITestRunner struct {
	recordingXrayRunner
	tags            map[string]bool
	calls           []string
	fail            string
	uncertain       bool
	badCompensation bool
	afterAdd        func()
}

func (r *liveAPITestRunner) API(_ context.Context, _ string, operation string, doc []byte) ([]byte, error) {
	r.calls = append(r.calls, operation)
	var document map[string][]json.RawMessage
	if operation == "adi" || operation == "rmi" {
		_ = json.Unmarshal(doc, &document)
	}
	if operation == "adi" {
		for _, in := range document["inbounds"] {
			r.tags[xrayEntryTag(in)] = true
		}
		if r.afterAdd != nil {
			r.afterAdd()
		}
	}
	if operation == "rmi" {
		for _, in := range document["inbounds"] {
			delete(r.tags, xrayEntryTag(in))
		}
	}
	if r.fail == operation {
		r.fail = ""
		if r.uncertain {
			return nil, errXrayAPIUncertain
		}
		return nil, errors.New("secret-not-for-logs")
	}
	if operation == "lsi" {
		if r.badCompensation {
			return []byte(`{}`), nil
		}
		in := []map[string]string{}
		for tag := range r.tags {
			in = append(in, map[string]string{"tag": tag})
		}
		return json.Marshal(map[string]any{"inbounds": in})
	}
	return nil, nil
}

func TestXrayLiveAdditionAndTransactionRollbackKeepProcess(t *testing.T) {
	r := &liveAPITestRunner{tags: map[string]bool{"old-in": true}}
	a := newTestXrayAdapter(t, r, true)
	first := liveAddPrepared(t, a, 1, false)
	if err := a.Commit(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	epoch := a.CounterEpoch()
	second := liveAddPrepared(t, a, 2, true)
	if err := a.Commit(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if err := a.Verify(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if strings.Join(r.calls, ",") != "ado,adrules,adi" {
		t.Fatal("incorrect API operation order")
	}
	if len(r.starts) != 1 || r.processes[0].stopCount != 0 || a.CounterEpoch() != epoch {
		t.Fatal("addition restarted old process")
	}
	if err := a.Rollback(context.Background(), &first); err != nil {
		t.Fatal(err)
	}
	if err := a.Verify(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if r.tags["new-in"] || !r.tags["old-in"] || len(r.starts) != 1 || a.CounterEpoch() != epoch {
		t.Fatal("rollback changed previous runtime")
	}
}

func TestXrayLiveFailureRestoresPreviousConfiguration(t *testing.T) {
	for _, stage := range []string{"ado", "adrules", "adi", "uncertain", "bad compensation", "disk"} {
		t.Run(stage, func(t *testing.T) {
			r := &liveAPITestRunner{tags: map[string]bool{"old-in": true}}
			a := newTestXrayAdapter(t, r, true)
			first := liveAddPrepared(t, a, 1, false)
			if err := a.Commit(context.Background(), first); err != nil {
				t.Fatal(err)
			}
			second := liveAddPrepared(t, a, 2, true)
			r.fail = stage
			if stage == "uncertain" {
				r.fail = "adi"
				r.uncertain = true
			}
			if stage == "bad compensation" {
				r.fail = "adi"
				r.badCompensation = true
			}
			if stage == "disk" {
				// Force the persistence boundary to fail after the RPC succeeds.
				r.afterAdd = func() { a.activePath = filepath.Join(a.activePath, "unwritable") }
			}
			originalPath := a.activePath
			err := a.Commit(context.Background(), second)
			if stage == "disk" {
				a.activePath = originalPath
			}
			if !errors.Is(err, ErrXrayLiveUpdate) || strings.Contains(err.Error(), "secret") {
				t.Fatalf("error = %v", err)
			}
			if err := a.Verify(context.Background(), first); err != nil {
				t.Fatal(err)
			}
			wantStarts := 1
			if stage == "uncertain" || stage == "bad compensation" {
				wantStarts = 2
			}
			if len(r.starts) != wantStarts {
				t.Fatalf("process starts = %d, want %d", len(r.starts), wantStarts)
			}
			if wantStarts == 1 && (r.tags["new-in"] || !r.tags["old-in"]) {
				t.Fatal("partial API addition not compensated")
			}
		})
	}
}
