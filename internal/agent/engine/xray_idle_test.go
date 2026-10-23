package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestXrayIdleProjectionPreservesGlobalsAndDesiredIdentity(t *testing.T) {
	r := &updateAPIRunner{in: map[string]json.RawMessage{}, out: map[string]json.RawMessage{}}
	a := newTestXrayAdapter(t, r, true)
	fragment := orderedLiveRule("rule", "in", "out", 18080)
	var document map[string]json.RawMessage
	_ = json.Unmarshal(fragment.Config, &document)
	document["log"] = json.RawMessage(`{"loglevel":"none"}`)
	document["policy"] = json.RawMessage(`{"levels":{"0":{"handshake":4}}}`)
	document["routing"] = json.RawMessage(`{"domainStrategy":"AsIs","rules":[{"type":"field","inboundTag":["in"],"outboundTag":"out"}]}`)
	fragment.Config, _ = json.Marshal(document)
	first := orderedLivePrepared(t, a, 1, fragment)
	ctx := context.Background()
	if err := a.Commit(ctx, first); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(first.adapterPath)
	var native map[string]json.RawMessage
	_ = json.Unmarshal(raw, &native)
	for key, entries := range map[string]map[string]json.RawMessage{"inbounds": r.in, "outbounds": r.out} {
		var values []json.RawMessage
		_ = json.Unmarshal(native[key], &values)
		for _, entry := range values {
			entries[xrayEntryTag(entry)] = entry
		}
	}
	desired := xrayDesired(t, 2, []agentv1.ConfigurationFragment{})
	for i := 0; i < 2; i++ {
		idle, err := a.Prepare(ctx, desired)
		if err != nil {
			t.Fatal(err)
		}
		if got := idle.DesiredNodeConfig(); !bytes.Equal(got.Config, desired.Config) || got.ConfigSHA256 != desired.ConfigSHA256 {
			t.Fatal("local idle projection changed control-plane identity")
		}
		if err := a.Commit(ctx, idle); err != nil {
			t.Fatal(err)
		}
		if err := a.Verify(ctx, idle); err != nil {
			t.Fatal(err)
		}
		var current map[string]json.RawMessage
		data, _ := os.ReadFile(a.activePath)
		_ = json.Unmarshal(data, &current)
		for _, key := range []string{"log", "policy", "api", "stats"} {
			if !sameXrayJSON(current[key], native[key]) {
				t.Fatalf("idle changed global %s", key)
			}
		}
		if len(r.in) != 1 || len(r.out) != 1 {
			t.Fatal("idle retained customer handlers or removed the API")
		}
	}
	if err := a.Rollback(ctx, &first); err != nil {
		t.Fatal(err)
	}
	if err := a.Verify(ctx, first); err != nil {
		t.Fatal(err)
	}
	if len(r.starts) != 1 || r.processes[0].stopCount != 0 {
		t.Fatal("idle/restore replaced the process")
	}
	if err := os.WriteFile(a.activePath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Prepare(ctx, desired); !errors.Is(err, ErrLiveUpdateRequiresMaintenance) {
		t.Fatalf("idle trusted a changed active file: %v", err)
	}
}
