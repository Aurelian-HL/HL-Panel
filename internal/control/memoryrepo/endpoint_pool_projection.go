package memoryrepo

import (
	"sync"
	"sync/atomic"

	"github.com/hongle/hl-panel/internal/control/endpoints"
)

type ruleEndpointPool struct {
	id        string
	ambiguous bool
}

// Built lazily from a read view's detached, immutable pools. Overlays share
// that same structure and index. Authoritative stores never use this index:
// their pool membership may change during a business transaction.
// This value must not be copied after first use.
type ruleEndpointPoolIndex struct {
	seen    atomic.Bool
	once    sync.Once
	entries map[string]ruleEndpointPool
}

func (index *ruleEndpointPoolIndex) lookup(pools map[string]endpoints.EndpointPool, ruleID string) ruleEndpointPool {
	// A view used for one proof should not pay to allocate a full index. The
	// first lookup scans; subsequent lookups amortize construction. Immutable
	// pools allow that first scan to overlap construction by another reader.
	if !index.seen.Load() && index.seen.CompareAndSwap(false, true) {
		var entry ruleEndpointPool
		found := false
		for id, pool := range pools {
			if pool.RuleID != ruleID {
				continue
			}
			if found {
				entry.ambiguous = true
				break
			}
			entry.id, found = id, true
		}
		return entry
	}
	index.once.Do(func() {
		entries := make(map[string]ruleEndpointPool, len(pools))
		for id, pool := range pools {
			entry, exists := entries[pool.RuleID]
			if exists {
				entry.ambiguous = true
			} else {
				entry.id = id
			}
			entries[pool.RuleID] = entry
		}
		index.entries = entries
	})
	return index.entries[ruleID]
}
