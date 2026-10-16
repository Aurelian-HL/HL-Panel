package deploymentreceipts

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestWarmBundleDoesNotTrustChangedRawOrDuplicateFragments(t *testing.T) {
	input := completeInput(t, agentv1.EngineXray)
	if !Evaluate(input).ReceiptVerified {
		t.Fatal("initial receipt rejected")
	}
	// Keep the declared hash, mutate bytes after warming the index.
	input.CurrentConfig.Config = json.RawMessage(`{"schema_version":1,"fragments":[]}`)
	if got := Evaluate(input); got.Reason != ReasonConfigInvalid {
		t.Fatalf("mutated raw with old hash: %#v", got)
	}
	fragment := agentv1.ConfigurationFragment{GroupID: "forwarding-vless/rule-one", GroupRevision: 3,
		Engine: agentv1.EngineXray, Config: json.RawMessage(`{"inbounds":[]}`)}
	setBundle(t, &input, []agentv1.ConfigurationFragment{fragment, fragment})
	for i := 0; i < 2; i++ {
		if got := Evaluate(input); got.Reason != ReasonFragmentMismatch || got.FragmentEmitted {
			t.Fatalf("duplicate fragment on pass %d: %#v", i, got)
		}
	}
}

func TestBundleIndexBoundsAndLRU(t *testing.T) {
	c := newBundleIndexCache(2, 4096)
	load := func(id string) *bundleIndex {
		t.Helper()
		input := completeInput(t, agentv1.EngineXray)
		setBundle(t, &input, []agentv1.ConfigurationFragment{{GroupID: id, Engine: agentv1.EngineXray,
			Config: json.RawMessage(`{}`)}})
		index, ok := c.get(input.CurrentConfig.ConfigSHA256, input.CurrentConfig.Config)
		if !ok {
			t.Fatal("valid bundle rejected")
		}
		return index
	}
	first, second := load("first"), load("second")
	if load("first") != first {
		t.Fatal("cache did not reuse index")
	}
	load("third")
	if len(c.entries) != 2 || c.bytes > c.maxBytes || load("first") != first || load("second") == second {
		t.Fatal("LRU entry bound failed")
	}
	c = newBundleIndexCache(10, 1)
	load("oversized")
	if len(c.entries) != 0 || c.bytes != 0 {
		t.Fatal("oversized bundle retained")
	}
	c = newBundleIndexCache(100, 1800)
	for i := 0; i < 30; i++ {
		load(fmt.Sprint(i))
		if c.bytes > c.maxBytes {
			t.Fatal("byte bound exceeded")
		}
	}
	if len(c.entries) >= 30 {
		t.Fatal("byte budget failed to evict")
	}
}

func TestInvalidBundleIndexIsNotRetained(t *testing.T) {
	c := newBundleIndexCache(2, 4096)
	for _, raw := range []string{`{`, `null`, `{"schema_version":2,"fragments":[]}`} {
		if _, ok := c.get(generations.SHA256Hex([]byte(raw)), []byte(raw)); ok {
			t.Fatalf("invalid schema accepted: %q", raw)
		}
	}
	if len(c.entries) != 0 {
		t.Fatal("invalid bundles retained")
	}
}

func TestBundleIndexConcurrentReadersKeepEvidenceCurrent(t *testing.T) {
	input := completeInput(t, agentv1.EngineXray)
	var wg sync.WaitGroup
	for n := 0; n < 32; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			local := input
			if n%2 == 1 {
				local.AttemptInvalidated = true
			}
			for i := 0; i < 100; i++ {
				got := Evaluate(local)
				if got.ReceiptVerified != (n%2 == 0) {
					t.Errorf("cached receipt crossed input boundary: %#v", got)
					return
				}
			}
		}(n)
	}
	wg.Wait()
}
