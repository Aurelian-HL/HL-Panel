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

func TestMonthlyRuleQuotaResetsAtUtcMonthBoundary(t *testing.T) {
	f := newVLESSBundleFixture(t, true)
	ctx := context.Background()
	jan31 := time.Date(2026, time.January, 31, 23, 59, 0, 0, time.UTC)
	rule := f.store.forwardRules[f.rule.ID]
	rule.TrafficLimitBytes = 100
	rule.TrafficQuotaMonthly = true
	f.store.forwardRules[rule.ID] = rule
	if err := f.store.recompileForwardingGroupsLocked([]string{rule.EntryGroupID}, jan31); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SyncRuleTraffic(ctx, rule.ID, 100, jan31); err != nil {
		t.Fatal(err)
	}
	if got := f.store.forwardRules[rule.ID].TrafficUsedBytes; got != 100 {
		t.Fatalf("January usage = %d, want 100", got)
	}
	if _, err := f.store.ForwardingRule(ctx, rule.ID); err != nil {
		t.Fatal(err)
	}
	feb1 := time.Date(2026, time.February, 1, 0, 1, 0, 0, time.UTC)
	if err := f.store.SyncRuleTraffic(ctx, rule.ID, 0, feb1); err != nil {
		t.Fatal(err)
	}
	current := f.store.forwardRules[rule.ID]
	if current.TrafficUsedBytes != 0 || current.TrafficUsagePeriod != "2026-02" {
		t.Fatalf("monthly period did not reset: used=%d period=%q", current.TrafficUsedBytes, current.TrafficUsagePeriod)
	}
	view, err := f.store.ForwardingRule(ctx, rule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Status == forwarding.StatusQuotaExhausted {
		t.Fatal("new month remained quota exhausted")
	}
	if err := f.store.SyncRuleTraffic(ctx, rule.ID, 10, feb1); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SyncRuleTraffic(ctx, rule.ID, 5, feb1); err != nil {
		t.Fatal(err)
	}
	if got := f.store.forwardRules[rule.ID].TrafficUsedBytes; got != 10 {
		t.Fatalf("same-month lower total overwrote usage: %d", got)
	}
}

func TestMonthlyRuleQuotaKeepsCurrentMonthUsageWhenLimitChanges(t *testing.T) {
	store, request := forwardingRepositoryFixture(t)
	service := forwarding.NewService(store, nil)
	ctx := context.Background()
	request.TrafficLimitBytes = 30
	request.TrafficQuotaMonthly = true
	rule, _, err := service.Create(ctx, "admin-test", request, "monthly-create")
	if err != nil {
		t.Fatal(err)
	}
	month := time.Date(2026, time.March, 10, 12, 0, 0, 0, time.UTC)
	if err := store.SyncRuleTraffic(ctx, rule.ID, 30, month); err != nil {
		t.Fatal(err)
	}
	current, err := store.ForwardingRule(ctx, rule.ID)
	if err != nil {
		t.Fatal(err)
	}
	request.Revision = current.Revision
	request.TrafficLimitBytes = 60
	updated, _, err := service.Update(ctx, "admin-test", rule.ID, request, "monthly-raise")
	if err != nil {
		t.Fatal(err)
	}
	if updated.TrafficUsedBytes != 30 || updated.Status == forwarding.StatusQuotaExhausted {
		t.Fatalf("raising monthly limit lost current-month usage or remained exhausted: %+v", updated)
	}
	if err := store.SyncRuleTraffic(ctx, rule.ID, 30, month); err != nil {
		t.Fatal(err)
	}
	if got := store.forwardRules[rule.ID].TrafficUsedBytes; got != 30 {
		t.Fatalf("same-month reconciliation changed usage: %d", got)
	}
}

func TestMonthlyRuleQuotaIgnoresDelayedPreviousMonthProjection(t *testing.T) {
	f := newVLESSBundleFixture(t, true)
	ctx := context.Background()
	rule := f.store.forwardRules[f.rule.ID]
	rule.TrafficLimitBytes, rule.TrafficQuotaMonthly = 100, true
	f.store.forwardRules[rule.ID] = rule
	february := time.Date(2026, time.February, 1, 0, 1, 0, 0, time.UTC)
	if err := f.store.SyncRuleTraffic(ctx, rule.ID, 10, february); err != nil {
		t.Fatal(err)
	}
	before := f.store.forwardRules[rule.ID]
	generation, audits := f.store.nodes[f.nodeID].DesiredGeneration, len(f.store.auditEvents)
	if err := f.store.SyncRuleTraffic(ctx, rule.ID, 100, february.Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	after := f.store.forwardRules[rule.ID]
	if after.TrafficUsagePeriod != before.TrafficUsagePeriod || after.TrafficUsedBytes != before.TrafficUsedBytes || after.Revision != before.Revision || !after.UpdatedAt.Equal(before.UpdatedAt) || f.store.nodes[f.nodeID].DesiredGeneration != generation || len(f.store.auditEvents) != audits {
		t.Fatalf("delayed January projection changed February quota: period=%s used=%d revision=%d", after.TrafficUsagePeriod, after.TrafficUsedBytes, after.Revision)
	}
}

func TestRuleTrafficProjectionIgnoresAccountingModeEdits(t *testing.T) {
	for _, monthly := range []bool{false, true} {
		store, request := forwardingRepositoryFixture(t)
		ctx := context.Background()
		service := forwarding.NewService(store, nil)
		request.TrafficLimitBytes, request.TrafficQuotaMonthly = 100, monthly
		rule, _, err := service.Create(ctx, "admin-test", request, "scope-create")
		if err != nil {
			t.Fatal(err)
		}
		projection := forwarding.RuleTrafficProjection{RuleID: rule.ID, ExpectedRevision: rule.Revision, Monthly: monthly, TotalBytes: 150, At: time.Now().UTC()}
		request.Revision, request.TrafficQuotaMonthly = rule.Revision, !monthly
		updated, _, err := service.Update(ctx, "admin-test", rule.ID, request, "scope-toggle")
		if err != nil {
			t.Fatal(err)
		}
		checkIgnored := func() {
			t.Helper()
			audits := len(store.auditEvents)
			if err := store.ApplyRuleTraffic(ctx, projection); err != nil {
				t.Fatal(err)
			}
			current := store.forwardRules[rule.ID]
			if current.TrafficUsedBytes != 0 || current.TrafficUsagePeriod != "" || current.Revision != updated.Revision || len(store.auditEvents) != audits {
				t.Fatalf("stale accounting scope charged the edited rule: monthly=%t used=%d", current.TrafficQuotaMonthly, current.TrafficUsedBytes)
			}
		}
		checkIgnored()
		// Switching back to the original mode must not make its old query valid.
		request.Revision, request.TrafficQuotaMonthly = updated.Revision, monthly
		updated, _, err = service.Update(ctx, "admin-test", rule.ID, request, "scope-toggle-back")
		if err != nil {
			t.Fatal(err)
		}
		checkIgnored()
		projection.ExpectedRevision = updated.Revision
		if err := store.ApplyRuleTraffic(ctx, projection); err != nil {
			t.Fatal(err)
		}
		if got := store.forwardRules[rule.ID].TrafficUsedBytes; got != 150 {
			t.Fatalf("current projection was ignored: %d", got)
		}
	}
}
