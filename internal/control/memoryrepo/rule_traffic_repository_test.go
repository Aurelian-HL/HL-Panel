package memoryrepo

import (
	"context"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	"testing"
	"time"
)

func TestRuleQuotaRecompilesListenerAndKeepsLedgerTotal(t *testing.T) {
	f := newVLESSBundleFixture(t, true)
	ctx := context.Background()
	now := time.Now().UTC()
	rule := f.store.forwardRules[f.rule.ID]
	rule.TrafficLimitBytes = 100
	f.store.forwardRules[rule.ID] = rule
	if err := f.store.recompileForwardingGroupsLocked([]string{rule.EntryGroupID}, now); err != nil {
		t.Fatal(err)
	}
	fragmentID := generations.ForwardingFragmentID(agentv1.EngineXray, rule.ID)
	hasListener := func() bool {
		fragments, err := f.store.compileForwardingFragmentsLocked(f.nodeID)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range fragments {
			if f.GroupID == fragmentID {
				return true
			}
		}
		return false
	}
	if !hasListener() {
		t.Fatal("initial listener unavailable")
	}
	generation := f.store.nodes[f.nodeID].DesiredGeneration
	if err := f.store.SyncRuleTraffic(ctx, rule.ID, 99, now); err != nil {
		t.Fatal(err)
	}
	if !hasListener() || f.store.nodes[f.nodeID].DesiredGeneration != generation {
		t.Fatal("paused before quota")
	}
	if err := f.store.SyncRuleTraffic(ctx, rule.ID, 100, now); err != nil {
		t.Fatal(err)
	}
	stopped, _ := f.store.ForwardingRule(ctx, rule.ID)
	if stopped.Status != forwarding.StatusQuotaExhausted || hasListener() || f.store.nodes[f.nodeID].DesiredGeneration != generation+1 {
		t.Fatal("quota failed to remove listener", stopped)
	}
	audits := len(f.store.auditEvents)
	for _, total := range []int64{100, 90, 105} {
		if err := f.store.SyncRuleTraffic(ctx, rule.ID, total, now); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.store.auditEvents) != audits || f.store.nodes[f.nodeID].DesiredGeneration != generation+1 || f.store.forwardRules[rule.ID].TrafficUsedBytes != 105 {
		t.Fatal("retry double enforced or decreased usage")
	}
	// Increasing the limit resumes the executable fragment with the same credentials.
	before := f.store.forwardRules[rule.ID]
	before.TrafficLimitBytes = 200
	f.store.forwardRules[rule.ID] = before
	if err := f.store.recompileForwardingGroupsLocked([]string{rule.EntryGroupID}, now); err != nil {
		t.Fatal(err)
	}
	if !hasListener() {
		t.Fatal("raised quota failed to restore listener")
	}
	before = f.store.forwardRules[rule.ID]
	before.Paused = true
	before.TrafficLimitBytes = 0
	f.store.forwardRules[rule.ID] = before
	if hasListener() {
		t.Fatal("unlimited quota bypassed manual pause")
	}
}
func TestRuleLimitEditsPreserveUsedBytesAndSnapshot(t *testing.T) {
	store, request := forwardingRepositoryFixture(t)
	ctx := context.Background()
	service := forwarding.NewService(store, nil)
	rule, _, err := service.CreateForAdministrator(ctx, "admin-test", request, "quota-create")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SyncRuleTraffic(ctx, rule.ID, 250, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	request.Revision = rule.Revision
	request.TrafficLimitBytes = 200
	rule, _, err = service.UpdateForAdministrator(ctx, "admin-test", rule.ID, request, "quota-lower")
	if err != nil || rule.Status != forwarding.StatusQuotaExhausted || rule.TrafficUsedBytes != 250 {
		t.Fatal("lowering limit did not preserve used bytes", rule, err)
	}
	raw, err := store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	durable, err := restored.ForwardingRule(ctx, rule.ID)
	if err != nil || durable.TrafficUsedBytes != 250 || durable.TrafficLimitBytes != 200 || durable.Status != forwarding.StatusQuotaExhausted {
		t.Fatal("quota lost after restart", err)
	}
	request.Revision = rule.Revision
	request.TrafficLimitBytes = 300
	rule, _, err = service.UpdateForAdministrator(ctx, "admin-test", rule.ID, request, "quota-raise")
	if err != nil || rule.Status == forwarding.StatusQuotaExhausted || rule.TrafficUsedBytes != 250 {
		t.Fatal("cannot recover", err)
	}
}
