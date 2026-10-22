package deploymentreceipts

import "github.com/hongle/hl-panel/internal/control/generations"

type evidenceKey struct {
	nodeID     string
	generation int64
	hash       string
}

// Evaluator belongs to one detached immutable configuration view. It validates
// each complete bundle once, retaining only its structural index. It never
// caches membership, attempts, receipts, readiness or time-dependent health.
// Callers must not mutate the configuration bytes in that view after creation.
// Mutable repositories must continue to use Evaluate.
type Evaluator struct{ bundles map[evidenceKey]*bundleIndex }

func NewEvaluator(configs []generations.NodeConfigGeneration) *Evaluator {
	e := &Evaluator{bundles: make(map[evidenceKey]*bundleIndex, len(configs))}
	for _, config := range configs {
		if !validBundleHash(&config) {
			continue
		}
		if index, ok := currentBundleIndexes.get(config.ConfigSHA256, config.Config); ok {
			e.bundles[evidenceKey{config.NodeID, config.Generation, config.ConfigSHA256}] = index
		}
	}
	return e
}

func (e *Evaluator) Evaluate(input Input) Status { return evaluate(input, e) }

func (e *Evaluator) bundle(config *generations.NodeConfigGeneration) (*bundleIndex, bool) {
	if e != nil {
		index, ok := e.bundles[evidenceKey{config.NodeID, config.Generation, config.ConfigSHA256}]
		return index, ok
	}
	if !validBundleHash(config) {
		return nil, false
	}
	return currentBundleIndexes.get(config.ConfigSHA256, config.Config)
}
