package migrationbackup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/idgen"
)

type RestoreInput struct {
	Bundle            Bundle
	AdministratorID   string
	AdministratorHash []byte
	Key               string
	Digest            string
	Event             audit.Event
	SaveRecovery      func(State) (string, error)
}
type Result struct {
	RecoveryID string    `json:"recovery_id"`
	Replayed   bool      `json:"replayed"`
	RestoredAt time.Time `json:"restored_at"`
}
type Repository interface {
	ExportMigration(context.Context) (State, error)
	RestoreMigration(context.Context, RestoreInput) (Result, error)
}
type Service struct {
	repo         Repository
	auth         *auth.Service
	audit        *audit.Service
	version      string
	runtime      func() RuntimeSecrets
	directory    string
	afterRestore func()
	quiesce      func() func()
	mu           sync.Mutex
}

func New(repo Repository, a *auth.Service, events *audit.Service, version, directory string, runtime func() RuntimeSecrets, after func()) *Service {
	return &Service{repo: repo, auth: a, audit: events, version: version, directory: directory, runtime: runtime, afterRestore: after}
}

// WithRestoreGuard stops background writers for the complete restore transaction.
// The returned cleanup resumes them only when the import did not commit.
func (s *Service) WithRestoreGuard(quiesce func() func()) *Service {
	s.quiesce = quiesce
	return s
}
func (s *Service) Export(ctx context.Context, adminID, adminPassword, password, sourceURL string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.auth.ConfirmAdministratorPassword(ctx, adminID, adminPassword); err != nil {
		return nil, err
	}
	data, err := s.repo.ExportMigration(ctx)
	if err != nil {
		return nil, err
	}
	b := Bundle{Manifest: Manifest{Format: 1, Version: s.version, CreatedAt: time.Now().UTC(), SourceURL: sourceURL}, State: data, Runtime: s.runtime()}
	raw, err := Seal(b, password)
	if err != nil {
		return nil, err
	}
	event, err := audit.NewEvent(time.Now().UTC(), "administrator", adminID, "panel.migration.export", "panel", "", "succeeded", map[string]any{"bytes": len(raw)})
	if err != nil {
		return nil, err
	}
	if err = s.audit.Record(ctx, event); err != nil {
		return nil, err
	}
	return raw, nil
}
func (s *Service) Preview(ctx context.Context, adminID, adminPassword, password string, raw []byte) (Preview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.auth.ConfirmAdministratorPassword(ctx, adminID, adminPassword); err != nil {
		return Preview{}, err
	}
	b, err := Open(raw, password, s.version)
	if err != nil {
		return Preview{}, err
	}
	return Describe(b, Digest(raw)), nil
}
func (s *Service) Restore(ctx context.Context, adminID, adminPassword, password, key, digest, targetURL string, raw []byte) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(key) < 8 || len(key) > 128 || digest != Digest(raw) {
		return Result{}, invalid("请先校验备份包，再确认恢复")
	}
	guard, err := s.auth.ConfirmAdministratorPassword(ctx, adminID, adminPassword)
	if err != nil {
		return Result{}, err
	}
	bundle, err := Open(raw, password, s.version)
	if err != nil {
		return Result{}, err
	}
	if s.quiesce != nil {
		resume := s.quiesce()
		defer resume()
	}
	event, err := audit.NewEvent(time.Now().UTC(), "administrator", adminID, "panel.migration.import", "panel", digest, "succeeded", map[string]any{"source_version": bundle.Manifest.Version, "browser_sessions_revoked": true})
	if err != nil {
		return Result{}, err
	}
	currentRuntime := s.runtime()
	result, err := s.repo.RestoreMigration(ctx, RestoreInput{Bundle: bundle, AdministratorID: adminID, AdministratorHash: guard, Key: key, Digest: digest, Event: event, SaveRecovery: func(current State) (string, error) {
		recovery := Bundle{Manifest: Manifest{Format: 1, Version: s.version, CreatedAt: time.Now().UTC(), SourceURL: targetURL}, State: current, Runtime: currentRuntime}
		encrypted, err := Seal(recovery, password)
		if err != nil {
			return "", err
		}
		return s.saveRecovery(encrypted)
	}})
	if err == nil && !result.Replayed && s.afterRestore != nil {
		s.afterRestore()
	}
	return result, err
}

var recoveryName = regexp.MustCompile(`^mbk_[a-f0-9]{32}$`)

func (s *Service) saveRecovery(raw []byte) (string, error) {
	if s.directory == "" {
		return "", errors.New("migration recovery directory unavailable")
	}
	if err := os.MkdirAll(s.directory, 0700); err != nil {
		return "", errors.New("cannot prepare migration recovery directory")
	}
	info, err := os.Lstat(s.directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return "", errors.New("unsafe migration recovery directory")
	}
	id, err := idgen.New("mbk")
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(filepath.Join(s.directory, id+".hlbackup"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", errors.New("cannot save migration recovery")
	}
	name := f.Name()
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(name)
		return "", errors.New("cannot persist migration recovery")
	}
	if runtime.GOOS != "windows" {
		d, err := os.Open(s.directory)
		if err != nil {
			return "", errors.New("cannot sync migration recovery directory")
		}
		err = d.Sync()
		d.Close()
		if err != nil {
			return "", errors.New("cannot sync migration recovery directory")
		}
	}
	return id, nil
}
func (s *Service) Recovery(id string) ([]byte, error) {
	if !recoveryName.MatchString(id) {
		return nil, faults.ErrNotFound
	}
	path := filepath.Join(s.directory, id+".hlbackup")
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return nil, faults.ErrNotFound
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, faults.ErrNotFound
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) || info.Size() > MaxArchiveBytes {
		return nil, faults.ErrNotFound
	}
	data := make([]byte, info.Size())
	if _, err = f.ReadAt(data, 0); err != nil {
		return nil, errors.New("cannot read migration recovery")
	}
	return data, nil
}
func DecodeRuntime(raw []byte) (RuntimeSecrets, error) {
	var value RuntimeSecrets
	if len(raw) > 4<<20 || json.Unmarshal(raw, &value) != nil || len(value.PasswordFingerprintKey) < 32 {
		return value, errors.New("invalid migration runtime secrets")
	}
	return value, nil
}
