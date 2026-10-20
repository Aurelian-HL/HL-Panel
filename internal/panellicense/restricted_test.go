package panellicense

import (
	"context"
	"testing"
	"time"
)

func TestUnconfiguredClientNeverGrantsManagementAndStopsCleanly(t *testing.T) {
	client := RestrictedClient()
	if s := client.Status("admin"); !s.Enabled || s.ManagementAllowed || s.Status != "unconfigured" || s.ReminderDue {
		t.Fatalf("unsafe status %+v", s)
	}
	if _, err := client.Prepare(context.Background(), "secret"); err == nil {
		t.Fatal("prepare accepted")
	}
	if client.Sync(context.Background()) == nil || client.Commit(context.Background()) == nil || client.Dismiss("admin") == nil {
		t.Fatal("unconfigured operation accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { client.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("run failed to stop")
	}
}
