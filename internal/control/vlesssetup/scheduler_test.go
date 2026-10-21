package vlesssetup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
)

type queuedNetworks struct {
	started chan string
	release chan struct{}
}

func (n queuedNetworks) Get(ctx context.Context, id string) (groupconfig.GroupNetwork, error) {
	n.started <- id
	select {
	case <-n.release:
		return groupconfig.GroupNetwork{}, errors.New("isolated setup interruption")
	case <-ctx.Done():
		return groupconfig.GroupNetwork{}, ctx.Err()
	}
}

func scheduledRule(i int) forwarding.Rule {
	rule := readyRule()
	rule.ID, rule.EntryGroupID = fmt.Sprintf("rule-%d", i), fmt.Sprintf("entry-%d", i)
	rule.Revision = 1
	return rule
}

func quietSetupLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func receiveSetup(t *testing.T, started <-chan string) string {
	t.Helper()
	select {
	case id := <-started:
		return id
	case <-time.After(2 * time.Second):
		t.Fatal("queued setup waited instead of draining immediately")
		return ""
	}
}

func waitSetupIdle(t *testing.T, service *Service) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		service.queueMu.Lock()
		idle := service.workers == 0 && len(service.queued) == 0
		service.queueMu.Unlock()
		if idle {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("setup workers did not release their slots")
}

func TestScheduleDrainsBurstWithoutPeriodicWaitAndCoalescesQueuedSaves(t *testing.T) {
	networks := queuedNetworks{make(chan string, 32), make(chan struct{})}
	service := NewService(networks, nil, nil)
	logger := quietSetupLogger()
	for i := range 2 {
		service.Schedule("admin-1", scheduledRule(i), logger)
	}
	seen := map[string]bool{receiveSetup(t, networks.started): true, receiveSetup(t, networks.started): true}
	for i := 2; i < 12; i++ {
		service.Schedule("admin-1", scheduledRule(i), logger)
	}
	for range 100 {
		service.Schedule("admin-1", scheduledRule(2), logger)
	}
	service.queueMu.Lock()
	workers, queued := service.workers, len(service.queue)
	service.queueMu.Unlock()
	if workers != 2 || queued != 10 {
		t.Fatalf("unbounded or duplicated queue: workers=%d queued=%d", workers, queued)
	}
	close(networks.release)
	for range 10 {
		id := receiveSetup(t, networks.started)
		if seen[id] {
			t.Fatal("queued duplicate was not coalesced")
		}
		seen[id] = true
	}
	waitSetupIdle(t, service)
	if len(seen) != 12 {
		t.Fatal("committed burst lost a scheduled rule")
	}
}

func TestScheduleOverflowIsBoundedAndWakesDurableRecovery(t *testing.T) {
	networks := queuedNetworks{make(chan string, setupQueueCapacity+2), make(chan struct{})}
	service := NewService(networks, nil, nil)
	logger := quietSetupLogger()
	for i := range 2 {
		service.Schedule("admin-1", scheduledRule(i), logger)
	}
	receiveSetup(t, networks.started)
	receiveSetup(t, networks.started)
	for i := 2; i < setupQueueCapacity+10; i++ {
		service.Schedule("admin-1", scheduledRule(i), logger)
	}
	service.queueMu.Lock()
	workers, queued := service.workers, len(service.queue)
	service.queueMu.Unlock()
	if workers != 2 || queued != setupQueueCapacity || len(service.wake) != 1 {
		t.Fatalf("overflow not bounded/recoverable: workers=%d queued=%d wake=%d", workers, queued, len(service.wake))
	}
	close(networks.release)
	waitSetupIdle(t, service)
}

type wakeRules struct{ listed chan struct{} }

func (r wakeRules) List(ctx context.Context) ([]forwarding.Rule, error) {
	r.listed <- struct{}{}
	return nil, ctx.Err()
}

func TestRunImmediatelyRecoversOnCoalescedNotification(t *testing.T) {
	service := NewService(nil, nil, nil)
	source := wakeRules{make(chan struct{}, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { service.Run(ctx, source, quietSetupLogger()); close(done) }()
	select {
	case <-source.listed:
	case <-time.After(time.Second):
		t.Fatal("initial recovery did not run")
	}
	service.Notify()
	select {
	case <-source.listed:
	case <-time.After(time.Second):
		t.Fatal("recovery still depends on the 15-second tick")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("recovery did not stop")
	}
}

type pausedRuleAccess struct{}

func (pausedRuleAccess) GetForAdministrator(context.Context, string, string) (forwarding.Rule, error) {
	rule := scheduledRule(0)
	rule.Paused = true
	return rule, nil
}
func (pausedRuleAccess) VLESSSetupAdministrator(context.Context, string) (string, error) {
	return "admin-1", nil
}

func TestScheduleReloadsCommittedPauseBeforeSetup(t *testing.T) {
	service := NewService(nil, nil, nil, WithRuleAccess(pausedRuleAccess{}))
	service.Schedule("admin-1", scheduledRule(0), quietSetupLogger())
	waitSetupIdle(t, service) // A stale save would call the nil network source.
}
