package postgressnapshot

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/groups"
)

func TestCounterProjectionAvoidsSnapshotWritesAndSurvivesEditsAndRestart(t *testing.T) {
	dsn := isolatedDatabaseURL(t)
	ctx := context.Background()
	admin := bootstrapAdministrator(t)
	writer, err := Open(ctx, dsn, admin)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { writer.Close() }()
	reader, err := Open(ctx, dsn, auth.Administrator{})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	now := time.Now().UTC()
	event := audit.Event{ID: "counter-fixture", ActorID: admin.ID, CreatedAt: now}
	if err = writer.CreateDeviceGroup(ctx, groups.DeviceGroup{ID: "entry", Name: "entry", Kind: groups.KindEntry}, event); err != nil {
		t.Fatal(err)
	}
	_, _, err = writer.UpdateGroupNetwork(ctx, groupconfig.UpdateInput{Network: groupconfig.GroupNetwork{GroupID: "entry", ConnectHost: "entry.example.test", PortStart: 22000, PortEnd: 22010, AllowDirect: true, TrafficMultiplier: 1, Revision: 1, UpdatedAt: now}, IdempotencyKey: "network", RequestSHA256: "network", CreatedBy: admin.ID}, event)
	if err != nil {
		t.Fatal(err)
	}
	service := forwarding.NewService(writer, nil)
	request := forwarding.Request{Name: "counter", EntryGroupID: "entry", EgressMode: forwarding.EgressDirect, Protocol: forwarding.ProtocolTCP, Targets: []forwarding.Target{{Host: "127.0.0.1", Port: 18080}}, SelectionPolicy: forwarding.SelectionRoundRobin, TrafficLimitBytes: 1000}
	rule, _, err := service.CreateForAdministrator(ctx, admin.ID, request, "create")
	if err != nil {
		t.Fatal(err)
	}
	revision := func() int64 {
		t.Helper()
		var n int64
		if err := writer.db.QueryRowContext(ctx, `SELECT revision FROM nyvp_control_snapshots`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := revision()
	var wg sync.WaitGroup
	errs := make(chan error, 24)
	for i := 1; i <= 24; i++ {
		wg.Add(1)
		go func(n int64) {
			defer wg.Done()
			errs <- writer.ApplyRuleTraffic(ctx, forwarding.RuleTrafficProjection{RuleID: rule.ID, ExpectedRevision: rule.Revision, TotalBytes: n, At: now})
		}(int64(i))
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if after := revision(); after != before {
		t.Fatalf("ordinary counters rewrote snapshot: %d -> %d", before, after)
	}
	state, err := reader.RuleTrafficState(ctx, rule.ID)
	if err != nil || state.TrafficUsedBytes != 24 {
		t.Fatal("cross-instance projection", state, err)
	}
	rules, err := reader.ListRuleTrafficStates(ctx)
	if err != nil || len(rules) != 1 || rules[0].TrafficUsedBytes != 24 {
		t.Fatal("list projection", err)
	}
	got, err := reader.ForwardingRule(ctx, rule.ID)
	if err != nil || got.TrafficUsedBytes != 24 {
		t.Fatal("normal read projection", got, err)
	}
	request.Revision = rule.Revision
	request.Name = "edited"
	edited, _, err := service.UpdateForAdministrator(ctx, admin.ID, rule.ID, request, "edit")
	if err != nil || edited.TrafficUsedBytes != 24 {
		t.Fatal("edit lost counter", edited, err)
	}
	if err = writer.ApplyRuleTraffic(ctx, forwarding.RuleTrafficProjection{RuleID: rule.ID, ExpectedRevision: rule.Revision, TotalBytes: 999, At: now}); err != nil {
		t.Fatal(err)
	}
	if err = writer.ApplyRuleTraffic(ctx, forwarding.RuleTrafficProjection{RuleID: rule.ID, ExpectedRevision: edited.Revision, TotalBytes: 30, At: now}); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	writer, err = Open(ctx, dsn, auth.Administrator{})
	if err != nil {
		t.Fatal(err)
	}
	got, err = writer.ForwardingRule(ctx, rule.ID)
	if err != nil || got.TrafficUsedBytes != 30 {
		t.Fatal("restart lost counter", got, err)
	}
	before = revision()
	if err = writer.ApplyRuleTraffic(ctx, forwarding.RuleTrafficProjection{RuleID: rule.ID, ExpectedRevision: edited.Revision, TotalBytes: 1000, At: now}); err != nil {
		t.Fatal(err)
	}
	got, err = reader.ForwardingRule(ctx, rule.ID)
	if err != nil || got.Status != forwarding.StatusQuotaExhausted || got.TrafficUsedBytes != 1000 || revision() != before+1 {
		t.Fatal("quota transition bypassed business transaction", got, err)
	}
}
