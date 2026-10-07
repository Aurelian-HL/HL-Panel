package engine

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var (
	ErrGOSTValidation = errors.New("gost configuration validation failed")
	ErrGOSTStart      = errors.New("gost process could not be started")
	ErrGOSTNotRunning = errors.New("gost process is not running")
)

type GOSTProcess interface {
	Stop(context.Context) error
	Running() bool
}

type GOSTRunner interface {
	Test(context.Context, string) error
	Start(context.Context, string) (GOSTProcess, error)
}

// ExecGOSTRunner uses fixed argv and discards engine diagnostics, which may
// otherwise reveal target addresses or future credential-bearing settings.
type ExecGOSTRunner struct {
	binaryPath string
}

func NewExecGOSTRunner(binaryPath string) (*ExecGOSTRunner, error) {
	binaryPath = filepath.Clean(strings.TrimSpace(binaryPath))
	if binaryPath == "." || !filepath.IsAbs(binaryPath) {
		return nil, errors.New("gost binary path must be absolute")
	}
	info, err := os.Lstat(binaryPath)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("gost binary path must be a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return nil, errors.New("gost binary is not executable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	version, err := exec.CommandContext(ctx, binaryPath, "-V").Output()
	if err != nil || !IsSupportedGOSTVersion(string(version)) {
		return nil, errors.New("gost binary must be version 3.3.1 or pinned v3.3.1-nightly.20260922")
	}
	return &ExecGOSTRunner{binaryPath: binaryPath}, nil
}

// IsSupportedGOSTVersion accepts only versions verified with the native compiler
// and process adapter. The pinned upstream nightly is distributed in releases.
func IsSupportedGOSTVersion(output string) bool {
	return strings.HasPrefix(output, "gost 3.3.1 (") ||
		strings.HasPrefix(output, "gost v3.3.1-nightly.20260922 (")
}

func (r *ExecGOSTRunner) Test(ctx context.Context, configPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, r.binaryPath, "-C", configPath, "-O", "json")
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return ErrGOSTValidation
	}
	return nil
}

func (r *ExecGOSTRunner) Start(ctx context.Context, configPath string) (GOSTProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	command := exec.Command(r.binaryPath, "-C", configPath)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return nil, ErrGOSTStart
	}
	process := &execGOSTProcess{command: command, done: make(chan struct{})}
	go func() {
		_ = command.Wait()
		close(process.done)
	}()
	select {
	case <-process.done:
		return nil, ErrGOSTStart
	default:
		return process, nil
	}
}

type execGOSTProcess struct {
	command *exec.Cmd
	done    chan struct{}
}

func (p *execGOSTProcess) PID() int {
	if p == nil || p.command == nil || p.command.Process == nil {
		return 0
	}
	return p.command.Process.Pid
}

func (p *execGOSTProcess) Running() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

func (p *execGOSTProcess) Stop(ctx context.Context) error {
	if !p.Running() {
		return nil
	}
	if err := p.command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
