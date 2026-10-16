package engine

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestValidatedCommitSkipsOnlyDuplicateNativeValidation(t *testing.T) {
	ctx := context.Background()
	t.Run("xray", func(t *testing.T) {
		runner := &recordingXrayRunner{}
		adapter := newTestXrayAdapter(t, runner, false)
		prepared := liveAddPrepared(t, adapter, 1, false)
		if err := adapter.Validate(ctx, prepared); err != nil {
			t.Fatal(err)
		}
		if err := adapter.CommitValidated(ctx, prepared); err != nil {
			t.Fatal(err)
		}
		if len(runner.tests) != 1 {
			t.Fatalf("native validation calls after validated commit = %d, want 1", len(runner.tests))
		}

		direct := liveAddPrepared(t, adapter, 2, false)
		if err := adapter.Commit(ctx, direct); err != nil {
			t.Fatal(err)
		}
		if len(runner.tests) != 2 {
			t.Fatalf("native validation calls after direct commit = %d, want 2", len(runner.tests))
		}
	})

	t.Run("gost", func(t *testing.T) {
		runner := &recordingGOSTRunner{}
		adapter := newTestGOSTAdapter(t, runner, false)
		prepared, err := adapter.Prepare(ctx, testDesired(t, 1, []agentv1.ConfigurationFragment{gostFragment("gost-rule", gostServiceA)}))
		if err != nil {
			t.Fatal(err)
		}
		if err := adapter.Validate(ctx, prepared); err != nil {
			t.Fatal(err)
		}
		if err := adapter.CommitValidated(ctx, prepared); err != nil {
			t.Fatal(err)
		}
		if len(runner.tests) != 1 {
			t.Fatalf("native validation calls after validated commit = %d, want 1", len(runner.tests))
		}

		direct, err := adapter.Prepare(ctx, testDesired(t, 2, []agentv1.ConfigurationFragment{gostFragment("gost-rule", gostServiceA)}))
		if err != nil {
			t.Fatal(err)
		}
		if err := adapter.Commit(ctx, direct); err != nil {
			t.Fatal(err)
		}
		if len(runner.tests) != 2 {
			t.Fatalf("native validation calls after direct commit = %d, want 2", len(runner.tests))
		}
	})
}

type observedCancelContext struct {
	context.Context
	checked chan struct{}
	calls   atomic.Int32
}

func (c *observedCancelContext) Err() error {
	if c.calls.Add(1) == 1 {
		close(c.checked)
	}
	return c.Context.Err()
}

func TestCommitValidatedChecksCancellationAfterWaitingForEngineLock(t *testing.T) {
	t.Run("xray", func(t *testing.T) {
		runner := &recordingXrayRunner{}
		adapter := newTestXrayAdapter(t, runner, true)
		first := liveAddPrepared(t, adapter, 1, false)
		if err := adapter.Commit(context.Background(), first); err != nil {
			t.Fatal(err)
		}
		second := liveAddPrepared(t, adapter, 2, true)
		if err := adapter.Validate(context.Background(), second); err != nil {
			t.Fatal(err)
		}
		beforeTests, beforeStarts, beforeStops := len(runner.tests), len(runner.starts), runner.processes[0].stopCount
		base, cancel := context.WithCancel(context.Background())
		ctx := &observedCancelContext{Context: base, checked: make(chan struct{})}
		adapter.mu.Lock()
		done := make(chan error, 1)
		go func() { done <- adapter.CommitValidated(ctx, second) }()
		<-ctx.checked
		cancel()
		adapter.mu.Unlock()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("CommitValidated() error = %v, want cancellation", err)
		}
		if len(runner.tests) != beforeTests || len(runner.starts) != beforeStarts || len(runner.processes) != beforeStarts || runner.processes[0].stopCount != beforeStops {
			t.Fatal("cancelled Xray commit changed the running engine")
		}
	})

	t.Run("gost", func(t *testing.T) {
		runner := &recordingGOSTRunner{}
		adapter := newTestGOSTAdapter(t, runner, true)
		first, err := adapter.Prepare(context.Background(), testDesired(t, 1, []agentv1.ConfigurationFragment{gostFragment("gost-rule", gostServiceA)}))
		if err != nil {
			t.Fatal(err)
		}
		if err := adapter.Commit(context.Background(), first); err != nil {
			t.Fatal(err)
		}
		secondConfig := strings.ReplaceAll(strings.ReplaceAll(gostServiceA, "forward-a", "forward-b"), "18081", "18082")
		second, err := adapter.Prepare(context.Background(), testDesired(t, 2, []agentv1.ConfigurationFragment{gostFragment("gost-rule", secondConfig)}))
		if err != nil {
			t.Fatal(err)
		}
		if err := adapter.Validate(context.Background(), second); err != nil {
			t.Fatal(err)
		}
		process := runner.processes[0]
		beforeTests, beforeStarts := len(runner.tests), len(runner.processes)
		base, cancel := context.WithCancel(context.Background())
		ctx := &observedCancelContext{Context: base, checked: make(chan struct{})}
		adapter.mu.Lock()
		done := make(chan error, 1)
		go func() { done <- adapter.CommitValidated(ctx, second) }()
		<-ctx.checked
		cancel()
		adapter.mu.Unlock()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("CommitValidated() error = %v, want cancellation", err)
		}
		if len(runner.tests) != beforeTests || len(runner.processes) != beforeStarts || !process.Running() {
			t.Fatal("cancelled GOST commit changed the running engine")
		}
	})
}

func TestCommitValidatedRejectsTamperedStagedFilesWithoutStoppingPreviousProcess(t *testing.T) {
	ctx := context.Background()
	t.Run("xray", func(t *testing.T) {
		runner := &recordingXrayRunner{}
		adapter := newTestXrayAdapter(t, runner, true)
		first := liveAddPrepared(t, adapter, 1, false)
		if err := adapter.Commit(ctx, first); err != nil {
			t.Fatal(err)
		}
		activeBefore, err := os.ReadFile(adapter.activePath)
		if err != nil {
			t.Fatal(err)
		}
		second := liveAddPrepared(t, adapter, 2, true)
		if err := adapter.Validate(ctx, second); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(second.adapterPath, []byte(`{"tampered":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := adapter.CommitValidated(ctx, second); !errors.Is(err, ErrConfigurationHash) {
			t.Fatalf("CommitValidated(tampered) error = %v, want ErrConfigurationHash", err)
		}
		activeAfter, err := os.ReadFile(adapter.activePath)
		if err != nil || !bytes.Equal(activeBefore, activeAfter) {
			t.Fatalf("active Xray configuration changed: error=%v", err)
		}
		if len(runner.processes) != 1 || runner.processes[0].stopCount != 0 {
			t.Fatalf("old Xray process was disturbed: starts=%d stops=%d", len(runner.processes), runner.processes[0].stopCount)
		}
	})

	t.Run("gost", func(t *testing.T) {
		runner := &recordingGOSTRunner{}
		adapter := newTestGOSTAdapter(t, runner, true)
		first, err := adapter.Prepare(ctx, testDesired(t, 1, []agentv1.ConfigurationFragment{gostFragment("gost-rule", gostServiceA)}))
		if err != nil {
			t.Fatal(err)
		}
		if err := adapter.Commit(ctx, first); err != nil {
			t.Fatal(err)
		}
		activeBefore, err := os.ReadFile(adapter.activePath)
		if err != nil {
			t.Fatal(err)
		}
		secondConfig := strings.ReplaceAll(strings.ReplaceAll(gostServiceA, "forward-a", "forward-b"), "18081", "18082")
		second, err := adapter.Prepare(ctx, testDesired(t, 2, []agentv1.ConfigurationFragment{gostFragment("gost-rule", secondConfig)}))
		if err != nil {
			t.Fatal(err)
		}
		if err := adapter.Validate(ctx, second); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(second.adapterPath, []byte(`{"tampered":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := adapter.CommitValidated(ctx, second); !errors.Is(err, ErrConfigurationHash) {
			t.Fatalf("CommitValidated(tampered) error = %v, want ErrConfigurationHash", err)
		}
		activeAfter, err := os.ReadFile(adapter.activePath)
		if err != nil || !bytes.Equal(activeBefore, activeAfter) {
			t.Fatalf("active GOST configuration changed: error=%v", err)
		}
		if len(runner.processes) != 1 || !runner.processes[0].Running() {
			t.Fatalf("old GOST process was disturbed: starts=%d", len(runner.processes))
		}
	})
}
