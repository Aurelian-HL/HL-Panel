package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunBackgroundSupervisorRestartsAfterTaskReturns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var starts atomic.Int32
	secondStarted := make(chan struct{})

	go runBackgroundSupervisor(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), []backgroundTask{
		{name: "probe", run: func(ctx context.Context) error {
			if starts.Add(1) == 2 {
				close(secondStarted)
			}
			if starts.Load() == 1 {
				return errors.New("transient failure")
			}
			<-ctx.Done()
			return ctx.Err()
		}},
	})

	select {
	case <-secondStarted:
		if got := starts.Load(); got != 2 {
			t.Fatalf("starts = %d, want 2", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("background task was not restarted")
	}
	cancel()
}

func TestRunBackgroundSupervisorStopsWithoutRestartOnParentCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var starts atomic.Int32
	done := make(chan struct{})
	go func() {
		runBackgroundSupervisor(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), []backgroundTask{
			{name: "usage", run: func(ctx context.Context) error {
				starts.Add(1)
				<-ctx.Done()
				return ctx.Err()
			}},
		})
		close(done)
	}()

	for starts.Load() == 0 {
		select {
		case <-time.After(time.Second):
			t.Fatal("background task did not start")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("supervisor did not stop after parent cancellation")
	}
	if got := starts.Load(); got != 1 {
		t.Fatalf("starts = %d after cancellation, want 1", got)
	}
}
