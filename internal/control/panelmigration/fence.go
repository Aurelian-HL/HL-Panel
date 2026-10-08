// Package panelmigration coordinates the fixed, SSH-based panel migration workflow.
package panelmigration

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

type FenceState struct {
	Role string `json:"role"`
	ID   string `json:"id"`
}

// Fence survives process restarts so the source cannot start writing after cutover.
type Fence struct {
	mu    sync.RWMutex
	path  string
	state FenceState
}

func OpenFence(directory string) (*Fence, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return nil, errors.New("unsafe migration directory")
	}
	f := &Fence{path: filepath.Join(directory, "fence.json")}
	raw, err := readPrivate(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil || json.Unmarshal(raw, &f.state) != nil || (f.state.Role != "source" && f.state.Role != "receiver") || !validID.MatchString(f.state.ID) {
		return nil, errors.New("invalid migration fence; manual recovery required")
	}
	return f, nil
}

func (f *Fence) State() FenceState { f.mu.RLock(); defer f.mu.RUnlock(); return f.state }
func (f *Fence) Active() bool      { return f.State().Role != "" }
func (f *Fence) Set(state FenceState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if state.Role != "source" || !validID.MatchString(state.ID) {
		return errors.New("invalid source fence")
	}
	if f.state.Role != "" && f.state != state {
		return errors.New("another migration owns the fence")
	}
	if err := writePrivate(f.path, state); err != nil {
		return err
	}
	f.state = state
	return nil
}
func (f *Fence) Clear(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state.Role == "" {
		return nil
	}
	if f.state.Role != "source" || f.state.ID != id {
		return errors.New("migration fence mismatch")
	}
	if err := os.Remove(f.path); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(f.path)); err != nil {
		return err
	}
	f.state = FenceState{}
	return nil
}

func (f *Fence) Allows(r *http.Request) bool {
	state := f.State()
	if state.Role == "" || r.URL.Path == "/healthz" {
		return true
	}
	if state.Role == "receiver" {
		if r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("Forwarded") != "" {
			return false
		}
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		ip := net.ParseIP(host)
		if err != nil || ip == nil || !ip.IsLoopback() {
			return false
		}
		return r.URL.Path == "/api/v1/auth/login" || r.URL.Path == "/api/v1/panel/migration/preview" || r.URL.Path == "/api/v1/panel/migration/import"
	}
	// Authenticated recovery remains reachable from the old IP after DNS moves.
	return r.URL.Path == "/api/v1/auth/login" || r.URL.Path == "/api/v1/auth/me" ||
		r.URL.Path == "/api/v1/panel/migration/auto" || r.URL.Path == "/api/v1/panel/migration/auto/probe" ||
		r.URL.Path == "/api/v1/panel/migration/auto/continue" || r.URL.Path == "/api/v1/panel/migration/auto/rollback" ||
		(r.Method == http.MethodGet && recoveryPath.MatchString(r.URL.Path))
}

func readPrivate(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 65536 || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return nil, errors.New("invalid migration metadata")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("migration metadata changed")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil {
		return nil, err
	}
	if len(raw) > 65536 {
		return nil, errors.New("migration metadata changed")
	}
	return raw, nil
}
func writePrivate(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".migration-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func syncDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
