package postgressnapshot

import (
	"context"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/groups"
)

func TestPostgreSQLRuleTrafficRejectsStalePeriodsAndModeRevisionsAcrossRestart(t *testing.T) {
	dsn := isolatedDatabaseURL(t)
	ctx := context.Background()
	admin := bootstrapAdministrator(t)
	store, err := Open(ctx, dsn, admin)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	january := time.Date(2026, time.January, 31, 23, 59, 0, 0, time.UTC)
	february := january.Add(2 * time.Minute)
	event := audit.Event{ID: "quota-fixture", ActorID: admin.ID, CreatedAt: january}
	if err = store.CreateDeviceGroup(ctx, groups.DeviceGroup{ID: "quota-entry", Name: "Quota entry", Kind: groups.KindEntry}, event); err != nil {
		t.Fatal(err)
	}
	_, _, err = store.UpdateGroupNetwork(ctx, groupconfig.UpdateInput{
		Network:        groupconfig.GroupNetwork{GroupID: "quota-entry", ConnectHost: "entry.example.test", PortStart: 22000, PortEnd: 22010, AllowDirect: true, TrafficMultiplier: 1, Revision: 1, UpdatedAt: january},
		IdempotencyKey: "quota-network", RequestSHA256: "quota-network", CreatedBy: admin.ID,
	}, event)
	if err != nil {
		t.Fatal(err)
	}
	service := forwarding.NewService(store, func() time.Time { return february })
	request := forwarding.Request{Name: "Monthly rule", EntryGroupID: "quota-entry", EgressMode: forwarding.EgressDirect, Protocol: forwarding.ProtocolTCP,
		Targets: []forwarding.Target{{Host: "127.0.0.1", Port: 18080}}, SelectionPolicy: forwarding.SelectionRoundRobin, TrafficLimitBytes: 100, TrafficQuotaMonthly: true}
	rule, _, err := service.CreateForAdministrator(ctx, admin.ID, request, "quota-create")
	if err != nil {
		t.Fatal(err)
	}
	apply := func(revision int64, monthly bool, total int64, at time.Time) {
		t.Helper()
		if err := store.ApplyRuleTraffic(ctx, forwarding.RuleTrafficProjection{RuleID: rule.ID, ExpectedRevision: revision, Monthly: monthly, TotalBytes: total, At: at}); err != nil {
			t.Fatal(err)
		}
	}
	load := func() forwarding.Rule {
		t.Helper()
		current, err := store.ForwardingRule(ctx, rule.ID)
		if err != nil {
			t.Fatal(err)
		}
		return current
	}
	apply(rule.Revision, true, 100, january)
	current := load()
	if current.Status != forwarding.StatusQuotaExhausted {
		t.Fatal("January quota was not enforced")
	}
	apply(current.Revision, true, 10, february)
	current = load()
	if current.TrafficUsedBytes != 10 || current.TrafficUsagePeriod != "2026-02" || current.Status == forwarding.StatusQuotaExhausted {
		t.Fatal("February quota did not reset", current)
	}
	// Use the current revision to verify the month guard itself, independently
	// of the stale-revision guard. Ignored projections must not append audit.
	beforeAudit, err := store.AuditEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	apply(current.Revision, true, 150, january)
	if after := load(); after.TrafficUsedBytes != 10 || after.TrafficUsagePeriod != "2026-02" || after.Revision != current.Revision {
		t.Fatal("delayed January total overwrote February", after)
	}
	for i, monthly := range []bool{false, true} {
		request.Revision, request.TrafficQuotaMonthly = load().Revision, monthly
		_, _, err = service.UpdateForAdministrator(ctx, admin.ID, rule.ID, request, []string{"quota-cumulative", "quota-monthly-again"}[i])
		if err != nil {
			t.Fatal(err)
		}
	}
	// Close and reopen before delivering the old query. Switching modes back
	// must not revive an old projection after a process restart either.
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, dsn, auth.Administrator{})
	if err != nil {
		t.Fatal(err)
	}
	apply(current.Revision, true, 150, february)
	if after := load(); after.TrafficUsedBytes != 0 || after.TrafficUsagePeriod != "" || after.Revision != current.Revision+2 {
		t.Fatal("stale revision revived after mode edits and restart", after)
	}
	afterAudit, err := store.AuditEvents(ctx)
	if err != nil || len(afterAudit) != len(beforeAudit)+2 {
		t.Fatal("ignored quota projections added audit events", err)
	}
	apply(load().Revision, true, 10, february)
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, dsn, auth.Administrator{})
	if err != nil {
		t.Fatal(err)
	}
	if after := load(); after.TrafficUsedBytes != 10 || after.TrafficUsagePeriod != "2026-02" {
		t.Fatal("current quota projection did not survive restart", after)
	}
}
