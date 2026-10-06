package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type GOSTAdapterOptions struct {
	StagingDir string
	ActivePath string
	BinaryPath string
	Runner     GOSTRunner
	AutoStart  bool
}

// GOSTProcessAdapter owns a GOST-only native document and, when explicitly
// enabled, one local process. It never interprets or drops Xray fragments.
type GOSTProcessAdapter struct {
	stagingDir string
	activePath string
	runner     GOSTRunner
	autoStart  bool

	counterEpoch uint64
	mu           sync.Mutex
	process      GOSTProcess
}

func NewGOSTProcessAdapter(options GOSTAdapterOptions) (*GOSTProcessAdapter, error) {
	stagingDir := filepath.Clean(strings.TrimSpace(options.StagingDir))
	activePath := filepath.Clean(strings.TrimSpace(options.ActivePath))
	if stagingDir == "." || !filepath.IsAbs(stagingDir) {
		return nil, errors.New("gost staging directory must be absolute")
	}
	if activePath == "." || !filepath.IsAbs(activePath) || activePath == stagingDir ||
		strings.HasPrefix(activePath, stagingDir+string(filepath.Separator)) {
		return nil, errors.New("gost active path must be an absolute file outside the staging directory")
	}
	runner := options.Runner
	if runner == nil {
		var err error
		runner, err = NewExecGOSTRunner(options.BinaryPath)
		if err != nil {
			return nil, err
		}
	}
	return &GOSTProcessAdapter{stagingDir: stagingDir, activePath: activePath, runner: runner, autoStart: options.AutoStart}, nil
}

func (a *GOSTProcessAdapter) Prepare(ctx context.Context, desired agentv1.DesiredNodeConfig) (PreparedConfiguration, error) {
	if err := ctx.Err(); err != nil {
		return PreparedConfiguration{}, err
	}
	prepared, err := parseDesired(desired)
	if err != nil {
		return PreparedConfiguration{}, err
	}
	native, err := CompileGOSTBundle(prepared.Bundle)
	if err != nil {
		return PreparedConfiguration{}, err
	}
	if err := ensurePrivateDir(a.stagingDir); err != nil {
		return PreparedConfiguration{}, fmt.Errorf("prepare gost staging directory: %w", err)
	}
	stagedPath := filepath.Join(a.stagingDir, fmt.Sprintf("node-bundle-%d-%s.json", prepared.Generation, prepared.ConfigSHA256[:16]))
	if err := writeFileAtomic(stagedPath, native, 0o600); err != nil {
		return PreparedConfiguration{}, fmt.Errorf("stage gost configuration: %w", err)
	}
	prepared.adapterPath = stagedPath
	return prepared, nil
}

func (a *GOSTProcessAdapter) Validate(ctx context.Context, prepared PreparedConfiguration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.verifyStaged(prepared); err != nil {
		return fmt.Errorf("validate staged gost configuration: %w", err)
	}
	if err := a.runner.Test(ctx, prepared.adapterPath); err != nil {
		return ErrGOSTValidation
	}
	return nil
}

func (a *GOSTProcessAdapter) Commit(ctx context.Context, prepared PreparedConfiguration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.Validate(ctx, prepared); err != nil {
		return err
	}
	candidate, err := readRegularFile(prepared.adapterPath, MaxConfigurationBytes)
	if err != nil {
		return fmt.Errorf("read staged gost configuration: %w", err)
	}
	previous, previousExists, err := readOptionalRegularFile(a.activePath)
	if err != nil {
		return fmt.Errorf("read active gost configuration: %w", err)
	}
	oldProcess := a.process
	oldRunning := oldProcess != nil && oldProcess.Running()
	if a.autoStart && oldRunning {
		if err := oldProcess.Stop(context.WithoutCancel(ctx)); err != nil {
			return fmt.Errorf("stop previous gost process: %w", err)
		}
		a.process = nil
	}
	if err := writeFileAtomic(a.activePath, candidate, 0o600); err != nil {
		if a.autoStart && oldRunning {
			_ = a.restartPrevious(context.WithoutCancel(ctx), previousExists)
		}
		return fmt.Errorf("activate gost configuration: %w", err)
	}
	if a.autoStart {
		started, startErr := a.startLocked(ctx, a.activePath)
		if startErr != nil {
			if restoreErr := a.restoreAfterStartFailure(context.WithoutCancel(ctx), previous, previousExists, oldRunning); restoreErr != nil {
				return fmt.Errorf("%w; rollback failed", startErr)
			}
			return startErr
		}
		a.process = started
	}
	return nil
}

func (a *GOSTProcessAdapter) Verify(ctx context.Context, prepared PreparedConfiguration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.checkOwner(prepared); err != nil {
		return err
	}
	active, err := readRegularFile(a.activePath, MaxConfigurationBytes)
	if err != nil {
		return fmt.Errorf("verify active gost configuration: %w", err)
	}
	expected, err := CompileGOSTBundle(prepared.Bundle)
	if err != nil || !bytes.Equal(active, expected) {
		return ErrConfigurationHash
	}
	if a.autoStart && (a.process == nil || !a.process.Running()) {
		return ErrGOSTNotRunning
	}
	return nil
}

// ActiveEngineMode attests only a process owned and currently running under
// this adapter. Staging or validating a configuration is not deployment.
func (a *GOSTProcessAdapter) ActiveEngineMode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.autoStart && a.process != nil && a.process.Running() {
		return "gost"
	}
	return ""
}

func (a *GOSTProcessAdapter) RequiresLiveProcess() bool { return a.autoStart }

func (a *GOSTProcessAdapter) Rollback(ctx context.Context, previous *PreparedConfiguration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.autoStart && a.process != nil && a.process.Running() {
		if err := a.process.Stop(context.WithoutCancel(ctx)); err != nil {
			return fmt.Errorf("stop gost process for rollback: %w", err)
		}
		a.process = nil
	}
	if previous == nil {
		return removeRegularFile(a.activePath)
	}
	if err := a.verifyStaged(*previous); err != nil {
		return fmt.Errorf("validate rollback gost configuration: %w", err)
	}
	data, err := readRegularFile(previous.adapterPath, MaxConfigurationBytes)
	if err != nil {
		return fmt.Errorf("read rollback gost configuration: %w", err)
	}
	if err := writeFileAtomic(a.activePath, data, 0o600); err != nil {
		return fmt.Errorf("restore gost configuration: %w", err)
	}
	if a.autoStart {
		started, err := a.startLocked(context.WithoutCancel(ctx), a.activePath)
		if err != nil {
			return err
		}
		a.process = started
	}
	return nil
}

func (a *GOSTProcessAdapter) Close(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.process == nil {
		return nil
	}
	err := a.process.Stop(ctx)
	a.process = nil
	return err
}

func (a *GOSTProcessAdapter) checkOwner(prepared PreparedConfiguration) error {
	if len(prepared.ConfigSHA256) < 16 {
		return ErrPreparedForOtherEngine
	}
	expected := filepath.Join(a.stagingDir, fmt.Sprintf("node-bundle-%d-%s.json", prepared.Generation, prepared.ConfigSHA256[:16]))
	if filepath.Clean(prepared.adapterPath) != expected {
		return ErrPreparedForOtherEngine
	}
	return nil
}

func (a *GOSTProcessAdapter) verifyStaged(prepared PreparedConfiguration) error {
	if err := a.checkOwner(prepared); err != nil {
		return err
	}
	data, err := readRegularFile(prepared.adapterPath, MaxConfigurationBytes)
	if err != nil {
		return err
	}
	expected, err := CompileGOSTBundle(prepared.Bundle)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, expected) {
		return ErrConfigurationHash
	}
	return nil
}

func (a *GOSTProcessAdapter) startLocked(ctx context.Context, path string) (GOSTProcess, error) {
	process, err := a.runner.Start(ctx, path)
	if err != nil || process == nil || !process.Running() {
		return nil, ErrGOSTStart
	}
	a.counterEpoch++
	return process, nil
}

func (a *GOSTProcessAdapter) restoreAfterStartFailure(ctx context.Context, previous []byte, exists, restart bool) error {
	if exists {
		if err := writeFileAtomic(a.activePath, previous, 0o600); err != nil {
			return err
		}
	} else if err := removeRegularFile(a.activePath); err != nil {
		return err
	}
	return a.restartPrevious(ctx, exists && restart)
}

func (a *GOSTProcessAdapter) restartPrevious(ctx context.Context, restart bool) error {
	if !restart {
		return nil
	}
	started, err := a.startLocked(ctx, a.activePath)
	if err != nil {
		return err
	}
	a.process = started
	return nil
}

// CounterEpoch changes only when this adapter starts a new owned process.
func (a *GOSTProcessAdapter) CounterEpoch() uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.counterEpoch
}
