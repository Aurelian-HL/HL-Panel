package healthprobe

import (
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/routing/endpointrouter"
)

func TestControllerKeepsProbeStateAndRouterVetoInSyncAcrossRevision(t *testing.T) {
	router, err := endpointrouter.New(endpointrouter.SelectionPolicyWeightedRoundRobin)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	controller, err := NewController(router, Config{
		Interval: time.Second, Timeout: time.Second,
		FailureThreshold: 1, SuccessThreshold: 1, MaxConcurrency: 1,
	}, func(_ Verdict, err error) {
		results <- err
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := endpointrouter.Endpoint{
		ID: "node-a", Address: "127.0.0.1:10001", Weight: 1,
		Status: endpointrouter.EndpointReady, HealthLeaseExpiresAt: time.Now().Add(time.Minute),
	}
	if err := controller.ReplaceVersionedMembership(1, []endpointrouter.Endpoint{endpoint}); err != nil {
		t.Fatal(err)
	}
	oldTargets := controller.monitor.probeTargets(time.Now())
	if len(oldTargets) != 1 {
		t.Fatalf("old target count = %d, want one", len(oldTargets))
	}
	controller.monitor.record(oldTargets[0], false)
	if err := waitControllerResult(t, results); err != nil {
		t.Fatal(err)
	}
	if blocked, err := router.LocallyBlocked(endpoint.ID); err != nil || !blocked {
		t.Fatalf("initial veto = %v, error = %v", blocked, err)
	}

	endpoint.HealthLeaseExpiresAt = time.Now().Add(2 * time.Minute)
	if err := controller.ReplaceVersionedMembership(2, []endpointrouter.Endpoint{endpoint}); err != nil {
		t.Fatal(err)
	}
	controller.monitor.record(oldTargets[0], true)
	select {
	case result := <-results:
		t.Fatalf("old revision emitted a result: %v", result)
	default:
	}
	if blocked, err := router.LocallyBlocked(endpoint.ID); err != nil || !blocked {
		t.Fatalf("veto after old success = %v, error = %v", blocked, err)
	}
	currentTargets := controller.monitor.probeTargets(time.Now())
	if len(currentTargets) != 1 || currentTargets[0].MembershipRevision != 2 {
		t.Fatalf("current targets = %#v", currentTargets)
	}
	controller.monitor.record(currentTargets[0], true)
	if err := waitControllerResult(t, results); err != nil {
		t.Fatal(err)
	}
	if blocked, err := router.LocallyBlocked(endpoint.ID); err != nil || blocked {
		t.Fatalf("veto after current success = %v, error = %v", blocked, err)
	}
}

func TestControllerNotifiesAfterTransitionLockIsReleased(t *testing.T) {
	router, err := endpointrouter.New(endpointrouter.SelectionPolicyWeightedRoundRobin)
	if err != nil {
		t.Fatal(err)
	}
	observerDone := make(chan error, 1)
	var controller *Controller
	controller, err = NewController(router, Config{
		Interval: time.Second, Timeout: time.Second,
		FailureThreshold: 1, SuccessThreshold: 1, MaxConcurrency: 1,
	}, func(_ Verdict, err error) {
		if err != nil {
			observerDone <- err
			return
		}
		if !controller.mu.TryLock() {
			observerDone <- errTransitionLockHeld
			return
		}
		controller.mu.Unlock()
		blocked, blockErr := router.LocallyBlocked("node-a")
		if blockErr != nil {
			observerDone <- blockErr
			return
		}
		if !blocked {
			observerDone <- errVetoNotApplied
			return
		}
		observerDone <- nil
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := endpointrouter.Endpoint{
		ID: "node-a", Address: "127.0.0.1:10001", Weight: 1,
		Status: endpointrouter.EndpointReady, HealthLeaseExpiresAt: time.Now().Add(time.Minute),
	}
	if err := controller.ReplaceVersionedMembership(1, []endpointrouter.Endpoint{endpoint}); err != nil {
		t.Fatal(err)
	}
	targets := controller.monitor.probeTargets(time.Now())
	controller.monitor.record(targets[0], false)
	if err := waitControllerResult(t, observerDone); err != nil {
		t.Fatal(err)
	}
}

var (
	errTransitionLockHeld = &controllerTestError{"transition lock held during result notification"}
	errVetoNotApplied     = &controllerTestError{"router veto was not applied before result notification"}
)

type controllerTestError struct{ message string }

func (e *controllerTestError) Error() string { return e.message }

func waitControllerResult(t *testing.T, results <-chan error) error {
	t.Helper()
	select {
	case err := <-results:
		return err
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for controller result")
		return nil
	}
}
