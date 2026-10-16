package engine

import (
	"context"
	"errors"
	"time"
)

// The process runners do not expose a protocol-specific health check. Keep a
// short observation window after Start so a process which exits just after its
// first heartbeat is rejected before the new configuration is reported as
// active. This is a process-liveness check, not proof that every listener is
// accepting traffic.
const (
	processReadinessWindow = 250 * time.Millisecond
	processReadinessPoll   = 10 * time.Millisecond
)

var errProcessNotReady = errors.New("engine process exited during startup")

type runningProcess interface {
	Running() bool
}

func waitForProcessReady(ctx context.Context, process runningProcess) error {
	if process == nil || !process.Running() {
		return errProcessNotReady
	}
	timer := time.NewTimer(processReadinessWindow)
	defer timer.Stop()
	ticker := time.NewTicker(processReadinessPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if !process.Running() {
				return errProcessNotReady
			}
		case <-timer.C:
			if !process.Running() {
				return errProcessNotReady
			}
			return nil
		}
	}
}

// stopFailedStartup prevents a runner from leaving a process behind after its
// readiness check fails. The bounded background context also works when the
// apply request itself has already timed out or been canceled.
func stopFailedStartup(process interface {
	Stop(context.Context) error
}) {
	if process == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = process.Stop(ctx)
}
