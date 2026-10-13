package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// backgroundTask is one of the control-plane loops that must remain alive for
// the panel to keep applying rules and publishing node health.
type backgroundTask struct {
	name string
	run  func(context.Context) error
}

type backgroundTaskResult struct {
	name string
	err  error
}

// runBackgroundSupervisor keeps the control-plane loops alive after an
// unexpected return. A failed loop cancels the current round so the services
// restart from a consistent snapshot instead of running with only part of the
// reconciliation pipeline active. Parent cancellation is the only normal
// exit, which preserves shutdown and migration isolation semantics.
func runBackgroundSupervisor(parent context.Context, logger *slog.Logger, tasks []backgroundTask) {
	if len(tasks) == 0 {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}

	backoff := time.Second
	for {
		if parent.Err() != nil {
			return
		}
		roundStarted := time.Now()
		roundContext, cancel := context.WithCancel(parent)
		results := make(chan backgroundTaskResult, len(tasks))
		var workers sync.WaitGroup
		for _, task := range tasks {
			workers.Add(1)
			go func(task backgroundTask) {
				defer workers.Done()
				results <- backgroundTaskResult{name: task.name, err: runBackgroundTask(roundContext, task)}
			}(task)
		}

		result := <-results
		cancel()
		workers.Wait()
		if parent.Err() != nil {
			return
		}

		logger.Warn("control-plane background service stopped; restarting",
			"service", result.name, "error", result.err)
		if time.Since(roundStarted) >= time.Minute {
			backoff = time.Second
		}
		timer := time.NewTimer(backoff)
		select {
		case <-parent.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}

func runBackgroundTask(ctx context.Context, task backgroundTask) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic: %v", recovered)
		}
	}()
	return task.run(ctx)
}
