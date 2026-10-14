package postgressnapshot

import (
	"sync"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/nodes"
)

func TestShouldPersistHeartbeatAfterClockRollback(t *testing.T) {
	store := &Store{heartbeats: make(map[string]heartbeatPersistence)}
	value := nodes.Heartbeat{Hostname: "node-a", AgentVersion: "1.0.0", CurrentAppliedGeneration: 7}
	first := time.Date(2026, time.October, 13, 10, 0, 0, 0, time.UTC)
	store.markHeartbeatPersisted("node-a", value, first)

	if store.shouldPersistHeartbeat("node-a", value, first.Add(5*time.Second)) {
		t.Fatal("heartbeat interval should still be throttled")
	}
	rolledBack := first.Add(-2 * time.Minute)
	if !store.shouldPersistHeartbeat("node-a", value, rolledBack) {
		t.Fatal("clock rollback must force the next heartbeat to persist")
	}
}

func TestShouldPersistHeartbeatStillUsesIntervalAfterStableClock(t *testing.T) {
	store := &Store{heartbeats: make(map[string]heartbeatPersistence)}
	value := nodes.Heartbeat{Hostname: "node-a", AgentVersion: "1.0.0", CurrentAppliedGeneration: 7}
	first := time.Date(2026, time.October, 13, 10, 0, 0, 0, time.UTC)
	store.markHeartbeatPersisted("node-a", value, first)

	if store.shouldPersistHeartbeat("node-a", value, first.Add(heartbeatSnapshotInterval-1)) {
		t.Fatal("heartbeat persisted too early")
	}
	if !store.shouldPersistHeartbeat("node-a", value, first.Add(heartbeatSnapshotInterval)) {
		t.Fatal("heartbeat interval expiry did not trigger persistence")
	}
}

func TestClaimHeartbeatPersistenceIsSingleFlight(t *testing.T) {
	store := &Store{heartbeats: make(map[string]heartbeatPersistence)}
	value := nodes.Heartbeat{Hostname: "node-a", AgentVersion: "1.0.0", CurrentAppliedGeneration: 7}
	now := time.Date(2026, time.October, 13, 10, 0, 0, 0, time.UTC)

	const callers = 32
	claims := make(chan bool, callers)
	var wait sync.WaitGroup
	wait.Add(callers)
	for range callers {
		go func() {
			defer wait.Done()
			claims <- store.claimHeartbeatPersistence("node-a", value, now)
		}()
	}
	wait.Wait()
	close(claims)

	count := 0
	for claimed := range claims {
		if claimed {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("concurrent heartbeat claims = %d, want exactly one", count)
	}

	store.releaseHeartbeatPersistence("node-a")
	if !store.claimHeartbeatPersistence("node-a", value, now) {
		t.Fatal("released heartbeat claim was not available for retry")
	}
	store.markHeartbeatPersisted("node-a", value, now)
	if store.claimHeartbeatPersistence("node-a", value, now.Add(time.Second)) {
		t.Fatal("stable heartbeat claimed again inside persistence interval")
	}
}

func TestFailedHeartbeatPersistenceClaimCanBeRetried(t *testing.T) {
	store := &Store{heartbeats: make(map[string]heartbeatPersistence)}
	value := nodes.Heartbeat{Hostname: "node-a", AgentVersion: "1.0.0"}
	now := time.Date(2026, time.October, 13, 10, 0, 0, 0, time.UTC)

	if !store.claimHeartbeatPersistence("node-a", value, now) {
		t.Fatal("initial heartbeat claim failed")
	}
	store.releaseHeartbeatPersistence("node-a")
	if !store.claimHeartbeatPersistence("node-a", value, now) {
		t.Fatal("failed persistence did not release claim for retry")
	}
}

func TestCompilerReconcileClaimBypassesIntervalButKeepsSingleFlight(t *testing.T) {
	store := &Store{heartbeats: make(map[string]heartbeatPersistence)}
	value := nodes.Heartbeat{Hostname: "node-a", AgentVersion: "1.0.0"}
	now := time.Date(2026, time.October, 14, 10, 0, 0, 0, time.UTC)
	store.markHeartbeatPersisted("node-a", value, now)
	if store.claimHeartbeatPersistence("node-a", value, now.Add(time.Second)) {
		t.Fatal("ordinary telemetry bypassed persistence interval")
	}
	if !store.claimHeartbeatCompilerReconcile("node-a", value, now.Add(time.Second)) {
		t.Fatal("compiler revision could not bypass persistence interval")
	}
	if store.claimHeartbeatCompilerReconcile("node-a", value, now.Add(time.Second)) {
		t.Fatal("concurrent reconciliation bypassed single-flight gate")
	}
	store.releaseHeartbeatPersistence("node-a")
	if !store.claimHeartbeatCompilerReconcile("node-a", value, now.Add(time.Second)) {
		t.Fatal("failed reconcile could not retry")
	}
}
