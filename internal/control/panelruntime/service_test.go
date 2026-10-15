package panelruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
)

type testController struct{ stops, restarts int }

func (c *testController) Stop(context.Context) error    { c.stops++; return nil }
func (c *testController) Restart(context.Context) error { c.restarts++; return nil }

type blockingController struct {
	started chan struct{}
	release chan struct{}
	calls   int
}

func (c *blockingController) Stop(context.Context) error { return nil }
func (c *blockingController) Restart(context.Context) error {
	c.calls++
	close(c.started)
	<-c.release
	return nil
}

type auditRecorder struct{ events []audit.Event }

func (r *auditRecorder) Record(_ context.Context, event audit.Event) error {
	r.events = append(r.events, event)
	return nil
}

func TestControlWithoutControllerIsExplicitlyFailed(t *testing.T) {
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	service := NewService("v1.2.3", nil, nil, func() time.Time { return now })
	result := service.Control(context.Background(), "restart")
	if result.Status != "failed" || result.Message == "" {
		t.Fatalf("expected explicit failure, got %+v", result)
	}
	if current, ok := service.LastControl(context.Background()); !ok || current.CommandID != result.CommandID {
		t.Fatalf("last result not retained: %+v %v", current, ok)
	}
}

func TestControlIdempotencySurvivesRestart(t *testing.T) {
	path := t.TempDir() + "/control.json"
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	controller := &testController{}
	first := NewService("v1.2.3", controller, nil, func() time.Time { return now }).WithStatePath(path)
	result, err := first.ControlWithIdempotency(context.Background(), "restart", "same-key")
	if err != nil || result.Status != "succeeded" || controller.restarts != 1 {
		t.Fatalf("first control failed: %+v %v", result, err)
	}
	reloaded := NewService("v1.2.3", controller, nil, func() time.Time { return now }).WithStatePath(path)
	replay, err := reloaded.ControlWithIdempotency(context.Background(), "restart", "same-key")
	if err != nil || replay.CommandID != result.CommandID || controller.restarts != 1 {
		t.Fatalf("control was not replayed: %+v %v", replay, err)
	}
	if _, err = reloaded.ControlWithIdempotency(context.Background(), "stop", "same-key"); !errors.Is(err, faults.ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
}

func TestControlIdempotencySerializesConcurrentRetries(t *testing.T) {
	controller := &blockingController{started: make(chan struct{}), release: make(chan struct{})}
	service := NewService("v1.2.3", controller, nil, time.Now)
	first := make(chan ControlResult, 1)
	go func() {
		result, err := service.ControlWithIdempotency(context.Background(), "restart", "same-key")
		if err != nil {
			t.Errorf("first control failed: %v", err)
		}
		first <- result
	}()
	<-controller.started
	second := make(chan ControlResult, 1)
	go func() {
		result, err := service.ControlWithIdempotency(context.Background(), "restart", "same-key")
		if err != nil {
			t.Errorf("retry failed: %v", err)
		}
		second <- result
	}()
	select {
	case <-second:
		t.Fatal("concurrent retry completed before the original operation")
	case <-time.After(25 * time.Millisecond):
	}
	close(controller.release)
	original := <-first
	replay := <-second
	if controller.calls != 1 || original.CommandID != replay.CommandID {
		t.Fatalf("retry executed more than once: calls=%d original=%+v replay=%+v", controller.calls, original, replay)
	}
}

func TestControlAuditIncludesAdministratorAndFailures(t *testing.T) {
	recorder := &auditRecorder{}
	controller := &testController{}
	service := NewService("v1.2.3", controller, nil, time.Now).WithAudit(recorder)
	if _, err := service.ControlWithIdempotencyAs(context.Background(), "restart", "audit-key", "admin-one"); err != nil {
		t.Fatal(err)
	}
	if len(recorder.events) != 1 || recorder.events[0].ActorID != "admin-one" || recorder.events[0].Outcome != "succeeded" {
		t.Fatalf("unexpected audit event: %+v", recorder.events)
	}
	// A nil controller is the deterministic failure path.
	failureService := NewService("v1.2.3", nil, nil, time.Now).WithAudit(recorder)
	if result, err := failureService.ControlWithIdempotencyAs(context.Background(), "restart", "failure-key", "admin-two"); err != nil || result.Status != "failed" {
		t.Fatalf("expected recorded command failure: %+v %v", result, err)
	}
	if len(recorder.events) != 2 || recorder.events[1].ActorID != "admin-two" || recorder.events[1].Outcome != "failed" {
		t.Fatalf("failure audit missing: %+v", recorder.events)
	}
}

func TestControlStateRejectsSymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.json")
	if err := os.WriteFile(target, []byte(`{"Key":"k"}`), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "control.json")
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	service := NewService("v1.2.3", nil, nil, time.Now).WithStatePath(path)
	if _, ok := service.LastControl(context.Background()); ok {
		t.Fatal("symlinked control state was loaded")
	}
}
