package memoryrepo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestUsageReadViewOwnsDetachedAccountingEvidence(t *testing.T) {
	f := newVLESSBundleFixture(t, true)
	if _, _, err := f.store.compileNodeConfigLocked(f.nodeID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	generation, _, _ := desiredVLESSBundle(t, f)
	expiry := time.Now().UTC().Add(time.Hour)
	sourceExpiry := expiry
	f.store.customers["usage-customer"] = customers.Customer{ID: "usage-customer", PasswordHash: []byte{1, 2}, ExpiresAt: &sourceExpiry}
	f.store.applyResultsByNode[f.nodeID] = make(map[int64]map[string]generations.ApplyResult)
	f.store.applyResultsByNode[f.nodeID][generation] = map[string]generations.ApplyResult{"commit": {ID: "receipt", Phase: agentv1.ApplyPhaseCommit}}
	view := f.store.UsageReadView()
	// The source can retain a transaction reader. A shared view must never call it.
	f.store.SetNodeConfigHistoryReader(func(context.Context, string, int64) (NodeConfigHistory, error) {
		t.Fatal("view retained transaction reader")
		return NodeConfigHistory{}, nil
	})
	c := f.store.customers["usage-customer"]
	c.PasswordHash[0] = 9
	*c.ExpiresAt = expiry.Add(time.Hour)
	f.store.nodeConfigsByNode[f.nodeID][generation].Config[0] = 'x'
	f.store.applyResultsByNode[f.nodeID][generation]["commit"] = generations.ApplyResult{ID: "uncommitted"}
	r := f.store.forwardRules[f.rule.ID]
	r.TrafficUsedBytes = 999
	f.store.forwardRules[r.ID] = r
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		customer, err := view.Customer(ctx, "usage-customer")
		if err != nil || customer.PasswordHash[0] != 1 || !customer.ExpiresAt.Equal(expiry) {
			t.Fatal("customer projection leaked mutation", err)
		}
		customer.PasswordHash[0] = 8
		*customer.ExpiresAt = expiry.Add(2 * time.Hour)
		input, err := view.UsageConfigInput(ctx, f.nodeID, generation)
		if err != nil || input.Config.Config[0] != '{' || input.ApplyResults[agentv1.ApplyPhaseCommit].ID != "receipt" {
			t.Fatal("config projection leaked mutation", err)
		}
		input.Config.Config[0] = 'y'
		delete(input.ApplyResults, agentv1.ApplyPhaseCommit)
		state, err := view.RuleTrafficState(ctx, f.rule.ID)
		if err != nil || state.TrafficUsedBytes == 999 {
			t.Fatal("traffic projection leaked mutation", err)
		}
		list, err := view.ListRuleTrafficStates(ctx)
		if err != nil || len(list) != 1 || list[0] != state {
			t.Fatal("inventory mismatch", err)
		}
		list[0].Revision = -1
	}
	if _, err := view.UsageConfigInput(ctx, f.nodeID, generation+100); !errors.Is(err, faults.ErrNotFound) {
		t.Fatal("missing generation accepted", err)
	}
}
