package deploymentreceipts

import (
	"container/list"
	"encoding/json"
	"strings"
	"sync"

	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// Cache only immutable structural evidence. Current membership, generations,
// attempts and receipts must still be checked by Evaluate on every call.
// Raw configuration and credentials are deliberately not retained.
var currentBundleIndexes = newBundleIndexCache(128, 16<<20)

type fragmentIndex struct {
	tunnelEntry, tunnelExit bool
	engine                  agentv1.Engine
	revision                int64
	count                   int
	valid                   bool
	hash                    string
}

type bundleIndex struct {
	fragments        map[string]fragmentIndex
	hasXray, hasGOST bool
}

type bundleIndexEntry struct {
	hash  string
	index *bundleIndex
	cost  int
}

type bundleIndexCache struct {
	mu                   sync.Mutex
	entries              map[string]*list.Element
	order                *list.List
	bytes                int
	maxEntries, maxBytes int
}

func newBundleIndexCache(maxEntries, maxBytes int) *bundleIndexCache {
	return &bundleIndexCache{entries: make(map[string]*list.Element), order: list.New(), maxEntries: maxEntries, maxBytes: maxBytes}
}

// The caller must verify SHA256(raw) == hash immediately before calling get.
// Returned indexes are private and immutable, including their maps.
func (c *bundleIndexCache) get(hash string, raw []byte) (*bundleIndex, bool) {
	c.mu.Lock()
	if item := c.entries[hash]; item != nil {
		c.order.MoveToFront(item)
		index := item.Value.(bundleIndexEntry).index
		c.mu.Unlock()
		return index, true
	}
	c.mu.Unlock()

	var bundle agentv1.ConfigurationBundle
	if err := json.Unmarshal(raw, &bundle); err != nil || bundle.SchemaVersion != 1 {
		return nil, false
	}
	index := &bundleIndex{fragments: make(map[string]fragmentIndex, len(bundle.Fragments))}
	// Charge raw size as well as conservative per-entry map/string overhead,
	// although raw bytes are not retained. Large bundles cannot grow the cache
	// without bound, even when the entry count is small.
	cost := len(raw) + len(hash) + 256
	for _, fragment := range bundle.Fragments {
		item := index.fragments[fragment.GroupID]
		if item.count == 0 {
			item.engine = fragment.Engine
			item.revision = int64(fragment.GroupRevision)
			item.valid = len(fragment.Config) != 0 && json.Valid(fragment.Config)
			if item.valid && fragment.Engine == agentv1.EngineGOST {
				var shape struct {
					Services []struct {
						Name     string
						Listener struct{ Type string }
						Handler  struct {
							Type  string
							Chain string
						}
					}
				}
				if json.Unmarshal(fragment.Config, &shape) == nil {
					id := strings.TrimPrefix(fragment.GroupID, generations.ForwardingGOSTFragmentPrefix)
					for _, service := range shape.Services {
						item.tunnelEntry = item.tunnelEntry || service.Name == "forward-"+id && service.Listener.Type == "tcp" && service.Handler.Chain == "tunnel-"+id
						item.tunnelExit = item.tunnelExit || service.Name == "tunnel-exit-"+id && service.Handler.Type == "relay" && (service.Listener.Type == "quic" || service.Listener.Type == "kcp")
					}
				}
			}
			if item.valid {
				// Include runtime configuration and rule-local metadata (including
				// identity, revision, engine, limits, and accounting ownership).
				// A change to any part of this fragment requires fresh evidence.
				rawFragment, err := json.Marshal(fragment)
				if err != nil {
					return nil, false
				}
				canonical, err := generations.CanonicalJSON(rawFragment)
				if err != nil {
					item.valid = false
				} else {
					item.hash = generations.SHA256Hex(canonical)
				}
			}
			cost += len(fragment.GroupID) + len(fragment.Engine) + 192
		}
		item.count++
		index.fragments[fragment.GroupID] = item
		index.hasXray = index.hasXray || fragment.Engine == agentv1.EngineXray
		index.hasGOST = index.hasGOST || fragment.Engine == agentv1.EngineGOST
	}
	if c.maxEntries <= 0 || cost > c.maxBytes {
		return index, true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if item := c.entries[hash]; item != nil {
		c.order.MoveToFront(item)
		return item.Value.(bundleIndexEntry).index, true
	}
	for len(c.entries) >= c.maxEntries || c.bytes+cost > c.maxBytes {
		oldest := c.order.Back()
		entry := oldest.Value.(bundleIndexEntry)
		delete(c.entries, entry.hash)
		c.bytes -= entry.cost
		c.order.Remove(oldest)
	}
	c.entries[hash] = c.order.PushFront(bundleIndexEntry{hash: hash, index: index, cost: cost})
	c.bytes += cost
	return index, true
}
