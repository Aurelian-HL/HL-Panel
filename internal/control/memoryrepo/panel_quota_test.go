package memoryrepo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/panelquota"
	"github.com/hongle/hl-panel/internal/control/rulegroups"
)

func freePanelLimits() panelquota.Limits { return panelquota.Limits{DeviceGroups: 1, Rules: 10} }

func TestPanelQuotaConcurrentGroupsAndReplay(t *testing.T) {
	s := New(auth.Administrator{ID: "admin"})
	s.SetPanelQuotaProvider(freePanelLimits)
	ctx := context.Background()
	var wg sync.WaitGroup
	var successes atomic.Int32
	var winner groups.CreateDeviceGroupInput
	var winnerMu sync.Mutex
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("group-%d", i)
			input := groups.CreateDeviceGroupInput{Group: groups.DeviceGroup{ID: id, Name: id, Kind: groups.KindEntry}, CreatedBy: "admin", IdempotencyKey: id, RequestSHA256: id}
			_, _, err := s.CreateDeviceGroupIdempotent(ctx, input, audit.Event{})
			if err == nil {
				successes.Add(1)
				winnerMu.Lock()
				winner = input
				winnerMu.Unlock()
			} else if !errors.Is(err, panelquota.ErrExceeded) {
				t.Errorf("unexpected group failure: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("created %d groups", successes.Load())
	}
	if _, replay, err := s.CreateDeviceGroupIdempotent(ctx, winner, audit.Event{}); err != nil || !replay {
		t.Fatalf("full quota blocked replay: %v", err)
	}
	usage, _ := s.PanelResourceUsage(ctx)
	if usage.DeviceGroups != 1 || len(s.AuditEvents()) != 1 {
		t.Fatal("rejected writes mutated group or audit state")
	}
}

func TestPanelQuotaRulesSharedAcrossOwnersProtocolsAndPaused(t *testing.T) {
	s, base := forwardingRepositoryFixture(t)
	s.SetPanelQuotaProvider(freePanelLimits)
	network := s.groupNetworks[base.EntryGroupID]
	network.PortEnd = 12100
	s.groupNetworks[base.EntryGroupID] = network
	service := forwarding.NewService(s, nil)
	ctx := context.Background()
	var wg sync.WaitGroup
	var successes atomic.Int32
	type created struct {
		key     string
		request forwarding.Request
		rule    forwarding.Rule
		admin   bool
	}
	winners := make(chan created, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			request := base
			request.Paused = i%2 == 0
			if i%3 == 0 {
				request.Protocol = forwarding.ProtocolUDP
			}
			key := fmt.Sprintf("rule-%d", i)
			var rule forwarding.Rule
			var err error
			admin := i%2 == 0
			if admin {
				rule, _, err = service.CreateForAdministrator(ctx, "admin-test", request, key)
			} else {
				rule, _, err = service.Create(ctx, "admin", request, key)
			}
			if err == nil {
				successes.Add(1)
				winners <- created{key, request, rule, admin}
			} else if !errors.Is(err, panelquota.ErrExceeded) {
				t.Errorf("unexpected rule failure: %v", err)
			}
		}(i)
	}
	wg.Wait()
	close(winners)
	if successes.Load() != 10 {
		t.Fatalf("created %d rules", successes.Load())
	}
	winner := <-winners
	var replay bool
	var err error
	if winner.admin {
		_, replay, err = service.CreateForAdministrator(ctx, "admin-test", winner.request, winner.key)
	} else {
		_, replay, err = service.Create(ctx, "admin", winner.request, winner.key)
	}
	if err != nil || !replay {
		t.Fatalf("rule replay rejected at quota: %v", err)
	}
	// Existing over-limit groups and paused rules remain maintainable and loadable.
	raw, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	restored.SetPanelQuotaProvider(freePanelLimits)
	winner.request.Name = "maintained"
	winner.request.Revision = winner.rule.Revision
	if winner.admin {
		_, _, err = forwarding.NewService(restored, nil).UpdateForAdministrator(ctx, "admin-test", winner.rule.ID, winner.request, "maintain")
	} else {
		_, _, err = forwarding.NewService(restored, nil).Update(ctx, "admin", winner.rule.ID, winner.request, "maintain")
	}
	if err != nil {
		t.Fatalf("maintenance rejected: %v", err)
	}
	usage, _ := restored.PanelResourceUsage(ctx)
	if usage.Rules != 10 {
		t.Fatal("paused rules or owners not counted")
	}
	// An actual deletion must release one slot; snapshot decoding itself imposes no quota.
	current, err := restored.ForwardingRule(ctx, winner.rule.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = rulegroups.NewService(restored, nil).Batch(ctx, "admin-test", rulegroups.BatchRequest{Operation: rulegroups.BatchDelete, RuleIDs: []string{current.ID}, ExpectedRevisions: map[string]int64{current.ID: current.Revision}}, "delete-slot")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = forwarding.NewService(restored, nil).CreateForAdministrator(ctx, "admin-test", base, "after-delete"); err != nil {
		t.Fatalf("deleted slot not reusable: %v", err)
	}
}

func TestPanelQuotaBulkAtomicityAndOverLimitMaintenance(t *testing.T) {
	s, base := forwardingRepositoryFixture(t)
	s.SetPanelQuotaProvider(freePanelLimits)
	network := s.groupNetworks[base.EntryGroupID]
	network.PortEnd = 12100
	s.groupNetworks[base.EntryGroupID] = network
	service := forwarding.NewService(s, nil)
	ctx := context.Background()
	lines := []string{}
	for i := 0; i < 11; i++ {
		lines = append(lines, fmt.Sprintf("r%d##landing.example.test#443", i))
	}
	input := legacyTransfer(base, strings.Join(lines, "\n"))
	if _, err := service.PreviewImport(ctx, input); !errors.Is(err, panelquota.ErrExceeded) {
		t.Fatalf("preview exceeded quota: %v", err)
	}
	if _, _, err := service.Import(ctx, "admin", input, "bulk"); !errors.Is(err, panelquota.ErrExceeded) {
		t.Fatalf("bulk exceeded quota: %v", err)
	}
	if len(s.forwardRules) != 0 || len(s.auditEvents) != 0 || len(s.businessIdempotency) != 0 {
		t.Fatal("failed bulk left side effects")
	}
	input.Content = strings.Join(lines[:10], "\n")
	if _, _, err := service.Import(ctx, "admin", input, "bulk"); err != nil {
		t.Fatalf("rejected request consumed idempotency: %v", err)
	}
	// A formerly licensed panel may already exceed free limits.
	s.SetPanelQuotaProvider(nil)
	extra, _, err := service.Create(ctx, "admin", base, "licensed-extra")
	if err != nil {
		t.Fatal(err)
	}
	s.SetPanelQuotaProvider(freePanelLimits)
	base.Revision = extra.Revision
	base.Name = "over-limit-maintenance"
	if _, _, err := service.Update(ctx, "admin", extra.ID, base, "edit-extra"); err != nil {
		t.Fatal(err)
	}
	base.Revision = 0
	if _, _, err := service.Create(ctx, "admin", base, "blocked-growth"); !errors.Is(err, panelquota.ErrExceeded) {
		t.Fatalf("over-limit expansion: %v", err)
	}
	usage, _ := s.PanelResourceUsage(ctx)
	if usage.Rules != 11 {
		t.Fatal("existing over-limit data lost")
	}
}
