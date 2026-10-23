package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type updateAPIRunner struct {
	recordingXrayRunner
	in, out            map[string]json.RawMessage
	fail               string
	calls              []string
	uncertain, badUndo bool
	failBefore         bool
	after              func()
}

func (r *updateAPIRunner) API(_ context.Context, _ string, operation string, doc []byte) ([]byte, error) {
	r.calls = append(r.calls, operation)
	if r.failBefore && r.fail == operation {
		r.fail = ""
		return nil, ErrXrayLiveUpdate
	}
	if operation == "lsi" || operation == "lso" {
		if r.badUndo && r.fail == "" {
			return []byte(`{}`), nil
		}
		key, entries := "inbounds", r.in
		if operation == "lso" {
			key, entries = "outbounds", r.out
		}
		items := []json.RawMessage{}
		for _, raw := range entries {
			items = append(items, raw)
		}
		return json.Marshal(map[string]any{key: items})
	}
	var document map[string][]json.RawMessage
	if operation != "adrules" {
		_ = json.Unmarshal(doc, &document)
	}
	key, entries := "inbounds", r.in
	if operation == "ado" || operation == "rmo" {
		key, entries = "outbounds", r.out
	}
	for _, raw := range document[key] {
		if operation == "rmi" || operation == "rmo" {
			delete(entries, xrayEntryTag(raw))
		} else {
			entries[xrayEntryTag(raw)] = raw
		}
	}
	if operation == "adi" && r.after != nil {
		r.after()
	}
	if r.fail == operation {
		r.fail = ""
		if r.uncertain {
			return nil, errXrayAPIUncertain
		}
		return nil, ErrXrayLiveUpdate
	}
	return nil, nil
}

func TestXrayFailureCompensatesOnlyAttemptedStages(t *testing.T) {
	for _, operation := range []string{"rmi", "ado", "adrules", "adi", "rmo"} {
		for _, before := range []bool{false, true} {
			name := operation + "/after"
			if before {
				name = operation + "/before"
			}
			t.Run(name, func(t *testing.T) {
				r := &updateAPIRunner{in: map[string]json.RawMessage{}, out: map[string]json.RawMessage{}}
				a := newTestXrayAdapter(t, r, true)
				old := orderedLivePrepared(t, a, 1, orderedLiveRule("a", "old-in", "old-out", 18080), orderedLiveRule("z", "steady-in", "steady-out", 18081))
				next := orderedLivePrepared(t, a, 2, orderedLiveRule("a", "old-in", "next-out", 18082), orderedLiveRule("z", "steady-in", "steady-out", 18081))
				if err := a.Commit(context.Background(), old); err != nil {
					t.Fatal(err)
				}
				raw, _ := os.ReadFile(old.adapterPath)
				var native map[string][]json.RawMessage
				// Only handler fields are arrays; ignore the other document fields.
				var document map[string]json.RawMessage
				if err := json.Unmarshal(raw, &document); err != nil {
					t.Fatal(err)
				}
				native = map[string][]json.RawMessage{}
				for key, entries := range map[string]map[string]json.RawMessage{"inbounds": r.in, "outbounds": r.out} {
					var nativeEntries []json.RawMessage
					if err := json.Unmarshal(document[key], &nativeEntries); err != nil {
						t.Fatal(err)
					}
					for _, entry := range nativeEntries {
						entries[xrayEntryTag(entry)] = entry
					}
					native[key] = append([]json.RawMessage{}, nativeEntries...)
				}
				r.fail, r.failBefore = operation, before
				if err := a.Commit(context.Background(), next); !errors.Is(err, ErrXrayLiveUpdate) {
					t.Fatalf("error=%v", err)
				}
				if r.fail != "" {
					t.Fatal("fault not exercised")
				}
				counts := map[string]int{}
				for _, call := range r.calls {
					counts[call]++
				}
				if operation == "rmi" || operation == "ado" {
					if counts["rmi"] != 1 || counts["adrules"] != 0 {
						t.Fatalf("unattempted listener/router replaced: %v", r.calls)
					}
				}
				if operation == "rmi" && counts["lso"] != 0 {
					t.Fatalf("untouched outbounds queried: %v", r.calls)
				}
				for key, entries := range map[string]map[string]json.RawMessage{"inbounds": r.in, "outbounds": r.out} {
					if len(entries) != len(native[key]) {
						t.Fatalf("%s size=%d want=%d", key, len(entries), len(native[key]))
					}
					for _, entry := range native[key] {
						if !sameXrayJSON(entries[xrayEntryTag(entry)], entry) {
							t.Fatalf("%s not restored", key)
						}
					}
				}
				if err := a.Verify(context.Background(), old); err != nil {
					t.Fatal(err)
				}
				if len(r.starts) != 1 || r.processes[0].stopCount != 0 {
					t.Fatal("failure restarted process")
				}
			})
		}
	}
}

func TestXrayLiveChangeRecoveryBoundaries(t *testing.T) {
	for _, stage := range []string{"confirmed", "uncertain", "compensation", "disk", "rollback"} {
		t.Run(stage, func(t *testing.T) {
			r := &updateAPIRunner{in: map[string]json.RawMessage{}, out: map[string]json.RawMessage{}}
			a := newTestXrayAdapter(t, r, true)
			old := orderedLivePrepared(t, a, 1, orderedLiveRule("a", "old-in", "old-out", 18080), orderedLiveRule("z", "steady-in", "steady-out", 18081))
			next := orderedLivePrepared(t, a, 2, orderedLiveRule("a", "old-in", "old-out", 18082), orderedLiveRule("z", "steady-in", "steady-out", 18081))
			if err := a.Commit(context.Background(), old); err != nil {
				t.Fatal(err)
			}
			raw, _ := os.ReadFile(old.adapterPath)
			var native map[string]json.RawMessage
			_ = json.Unmarshal(raw, &native)
			// Seed fake runtime from the actual compiled configuration.
			for key, entries := range map[string]map[string]json.RawMessage{"inbounds": r.in, "outbounds": r.out} {
				var values []json.RawMessage
				_ = json.Unmarshal(native[key], &values)
				for _, v := range values {
					entries[xrayEntryTag(v)] = v
				}
			}
			original := a.activePath
			r.fail = "adi"
			r.uncertain = stage == "uncertain"
			r.badUndo = stage == "compensation"
			if stage == "disk" {
				r.fail = ""
				r.after = func() { a.activePath = filepath.Join(original, "unwritable") }
			}
			if stage == "rollback" {
				r.fail = ""
				if err := a.Commit(context.Background(), next); err != nil {
					t.Fatal(err)
				}
				r.fail = "adi"
				if err := a.Rollback(context.Background(), &old); !errors.Is(err, ErrXrayLiveUpdate) {
					t.Fatalf("failed rollback was attested: %v", err)
				}
			} else {
				if err := a.Commit(context.Background(), next); !errors.Is(err, ErrXrayLiveUpdate) {
					t.Fatalf("update error: %v", err)
				}
			}
			a.activePath = original
			if stage == "uncertain" || stage == "compensation" || stage == "disk" {
				for _, op := range []func() error{
					func() error { return a.Verify(context.Background(), old) },
					func() error { return a.Commit(context.Background(), next) },
					func() error { return a.Rollback(context.Background(), &old) },
				} {
					if err := op(); !errors.Is(err, ErrLiveStateUncertain) {
						t.Fatalf("uncertain state accepted: %v", err)
					}
				}
			} else {
				verified := old
				if stage == "rollback" {
					verified = next // confirmed compensation retained the current generation
				}
				if err := a.Verify(context.Background(), verified); err != nil {
					t.Fatal(err)
				}
			}
			if len(r.starts) != 1 || r.processes[0].stopCount != 0 || r.in["steady-in"] == nil || r.out["steady-out"] == nil {
				t.Fatal("failure recovery disturbed unrelated handler or process")
			}
			if stage == "confirmed" || stage == "rollback" {
				for _, operation := range r.calls {
					if operation == "adrules" {
						t.Fatal("unchanged routing was globally reloaded during compensation")
					}
				}
			}
		})
	}
}

func TestXrayLivePlannerRejectsGlobalAndAmbiguousRoutingChanges(t *testing.T) {
	a := newTestXrayAdapter(t, &recordingXrayRunner{}, false)
	old := orderedLivePrepared(t, a, 1, orderedLiveRule("a", "old-in", "old-out", 18080), orderedLiveRule("z", "steady-in", "steady-out", 18081))
	next := orderedLivePrepared(t, a, 2, orderedLiveRule("z", "steady-in", "steady-out", 18081))
	before, _ := os.ReadFile(old.adapterPath)
	after, _ := os.ReadFile(next.adapterPath)
	if _, ok := planXrayLiveChanges(before, after); !ok {
		t.Fatal("managed deletion not eligible")
	}
	for name, change := range map[string]func(map[string]any){
		"global policy": func(m map[string]any) { m["policy"] = map[string]any{"system": map[string]any{}} },
		"API services":  func(m map[string]any) { m["api"].(map[string]any)["services"] = []string{"StatsService"} },
		"API listener": func(m map[string]any) {
			for _, v := range m["inbounds"].([]any) {
				i := v.(map[string]any)
				if i["tag"] == "hl-stats-api-in" {
					i["port"] = 19100
				}
			}
		},
		"routing settings": func(m map[string]any) { m["routing"].(map[string]any)["domainStrategy"] = "IPIfNonMatch" },
		"missing fallback": func(m map[string]any) { r := m["routing"].(map[string]any); r["rules"] = r["rules"].([]any)[:1] },
		"conditional fallback": func(m map[string]any) {
			r := m["routing"].(map[string]any)["rules"].([]any)
			r[len(r)-1].(map[string]any)["domain"] = []string{"example.com"}
		},
		"global route": func(m map[string]any) {
			r := m["routing"].(map[string]any)
			r["rules"] = append(r["rules"].([]any), map[string]any{"type": "field", "outboundTag": "steady-out"})
		},
		"unknown inbound": func(m map[string]any) {
			r := m["routing"].(map[string]any)
			r["rules"] = append(r["rules"].([]any), map[string]any{"type": "field", "inboundTag": []string{"unknown"}, "outboundTag": "steady-out"})
		},
		"duplicate tags": func(m map[string]any) { v := m["inbounds"].([]any); m["inbounds"] = append(v, v[0]) },
	} {
		t.Run(name, func(t *testing.T) {
			var doc map[string]any
			_ = json.Unmarshal(after, &doc)
			change(doc)
			data, _ := json.Marshal(doc)
			if _, ok := planXrayLiveChanges(before, data); ok {
				t.Fatal("unsafe plan accepted")
			}
		})
	}
}

func TestSameTagOutboundReplacementKeepsLastGoodProcess(t *testing.T) {
	r := &updateAPIRunner{in: map[string]json.RawMessage{}, out: map[string]json.RawMessage{}}
	a := newTestXrayAdapter(t, r, true)
	oldRule := orderedLiveRule("a", "old-in", "old-out", 18080)
	old := orderedLivePrepared(t, a, 1, oldRule)
	var edited map[string]any
	if err := json.Unmarshal(oldRule.Config, &edited); err != nil {
		t.Fatal(err)
	}
	edited["outbounds"].([]any)[0].(map[string]any)["settings"] = map[string]any{"redirect": "127.0.0.1:19000"}
	oldRule.Config, _ = json.Marshal(edited)
	next := orderedLivePrepared(t, a, 2, oldRule)
	before, _ := os.ReadFile(old.adapterPath)
	after, _ := os.ReadFile(next.adapterPath)
	if _, ok := planXrayLiveChanges(before, after); ok {
		t.Fatal("remove-before-add replacement accepted")
	}
	if err := a.Commit(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	if err := a.Commit(context.Background(), next); !errors.Is(err, ErrLiveUpdateRequiresMaintenance) {
		t.Fatalf("unsafe edit not refused: %v", err)
	}
	if err := a.Verify(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	if len(r.starts) != 1 || r.processes[0].stopCount != 0 {
		t.Fatal("refusal restarted the last-good process")
	}
}
