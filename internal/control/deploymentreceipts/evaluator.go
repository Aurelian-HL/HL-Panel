package deploymentreceipts

import (
	"sync"

	"github.com/hongle/hl-panel/internal/control/generations"
)

type evidenceKey struct {
	nodeID     string
	generation int64
	hash       string
}

// Evaluator belongs to one detached immutable configuration view. It validates
// each complete bundle once on first use. Publishing a configuration-only read
// view does not hash or parse unrelated node bundles. It never
// caches membership, attempts, receipts, readiness or time-dependent health.
// Callers must not mutate the configuration bytes in that view after creation.
// Mutable repositories must continue to use Evaluate.
type Evaluator struct {
	bundles map[evidenceKey]*deferredBundle
}

type deferredBundle struct {
	once  sync.Once
	raw   []byte
	hash  string
	index *bundleIndex
}

func (b *deferredBundle) load() (*bundleIndex, bool) {
	b.once.Do(func() {
		if len(b.hash) == 64 && generations.SHA256Hex(b.raw) == b.hash {
			b.index, _ = currentBundleIndexes.get(b.hash, b.raw)
		}
		// The structural index contains no credentials. Release this reference
		// once validation finishes; the owning immutable view retains its bytes.
		b.raw = nil
	})
	return b.index, b.index != nil
}

func NewEvaluator(configs []generations.NodeConfigGeneration) *Evaluator {
	e := &Evaluator{bundles: make(map[evidenceKey]*deferredBundle, len(configs))}
	// Stable slice elements keep sync.Once private to each bundle while avoiding
	// one heap allocation per node on every immutable read-view publication.
	entries := make([]deferredBundle, len(configs))
	for i, config := range configs {
		key := evidenceKey{config.NodeID, config.Generation, config.ConfigSHA256}
		entry := &entries[i]
		entry.raw, entry.hash = config.Config, config.ConfigSHA256
		if e.bundles[key] != nil {
			// Preserve the previous constructor's duplicate-key semantics: an
			// invalid later record cannot replace an earlier valid record.
			if _, ok := entry.load(); !ok {
				continue
			}
		}
		e.bundles[key] = entry
	}
	return e
}

func (e *Evaluator) Evaluate(input Input) Status { return evaluate(input, e) }

func (e *Evaluator) bundle(config *generations.NodeConfigGeneration) (*bundleIndex, bool) {
	if e != nil {
		entry := e.bundles[evidenceKey{config.NodeID, config.Generation, config.ConfigSHA256}]
		if entry == nil {
			return nil, false
		}
		return entry.load()
	}
	if !validBundleHash(config) {
		return nil, false
	}
	return currentBundleIndexes.get(config.ConfigSHA256, config.Config)
}
