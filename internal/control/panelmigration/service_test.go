package panelmigration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/migrationbackup"
)

type fakeRepository struct {
	state   migrationbackup.State
	exports atomic.Int32
}

func (r *fakeRepository) ExportMigration(context.Context) (migrationbackup.State, error) {
	r.exports.Add(1)
	return r.state, nil
}
func (r *fakeRepository) RestoreMigration(context.Context, migrationbackup.RestoreInput) (migrationbackup.Result, error) {
	return migrationbackup.Result{}, errors.New("source must never import")
}

type fakeTransport struct {
	mu      sync.Mutex
	actions []string
	fail    string
	stopped atomic.Bool
}

func (f *fakeTransport) Probe(context.Context, Target) (string, error) {
	return "SHA256:" + strings.Repeat("a", 43), nil
}
func (f *fakeTransport) Run(ctx context.Context, target Target, credentials Credentials, input RemoteInput) (RemoteResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actions = append(f.actions, input.Action)
	if credentials.Password == "" || target.Fingerprint == "" {
		return RemoteResult{}, errors.New("missing SSH credentials or pin")
	}
	if f.fail == input.Action {
		f.fail = ""
		return RemoteResult{}, errors.New("private diagnostic must never reach UI")
	}
	switch input.Action {
	case "prepare":
		return RemoteResult{State: "prepared"}, nil
	case "restore":
		if len(input.Archive) == 0 || input.Digest != migrationbackup.Digest(input.Archive) || input.Counts["administrators"] != 1 {
			return RemoteResult{}, errors.New("bad final archive")
		}
		return RemoteResult{State: "restored", RecoveryID: "target-backup"}, nil
	case "activate":
		return RemoteResult{State: "completed"}, nil
	case "rollback":
		f.stopped.Store(true)
		return RemoteResult{State: "cancelled"}, nil
	}
	return RemoteResult{}, errors.New("unknown action")
}
func fixtureService(t *testing.T) (*Service, *fakeRepository, *fakeTransport, Input) {
	t.Helper()
	directory := t.TempDir()
	os.Chmod(directory, 0700)
	fence, err := OpenFence(filepath.Join(directory, "task"))
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("isolated-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	store := memoryrepo.New(auth.Administrator{ID: "admin-one", Username: "admin", PasswordHash: hash, CreatedAt: time.Now()})
	events := audit.NewService(store)
	authentication := auth.NewService(store, events, time.Now, time.Hour)
	snapshot, err := store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	repository := &fakeRepository{state: migrationbackup.State{Snapshot: snapshot, Usage: map[string]json.RawMessage{}}}
	for _, table := range migrationbackup.Tables() {
		repository.state.Usage[table] = json.RawMessage("[]")
	}
	backup := migrationbackup.New(repository, authentication, events, "v0.1.50", filepath.Join(directory, "backups"), func() migrationbackup.RuntimeSecrets {
		return migrationbackup.RuntimeSecrets{PasswordFingerprintKey: bytes.Repeat([]byte{1}, 32)}
	}, nil)
	transport := &fakeTransport{}
	config := Config{Directory: filepath.Join(directory, "task"), Domain: "panel.example.com", SourceIPURL: "https://1.1.1.1:443", Version: "v0.1.50", Installer: "fixed --migration-receiver installer", Fence: fence, Transport: transport,
		Freeze: func(id string) error { return fence.Set(FenceState{Role: "source", ID: id}) },
		Thaw: func(id string) error {
			if !transport.stopped.Load() {
				return errors.New("target was not stopped first")
			}
			return fence.Clear(id)
		}}
	service, err := New(authentication, events, backup, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	input := Input{ID: testID, Target: Target{Host: "8.8.8.8", Port: 22, Fingerprint: "SHA256:" + strings.Repeat("a", 43)}, Credentials: Credentials{Password: "isolated-ssh-secret"}, AdministratorPassword: "isolated-admin-password", Password: "isolated-backup-password", SourceURL: "https://panel.example.com", Confirm: "PREPARE"}
	return service, repository, transport, input
}
func awaitState(t *testing.T, s *Service, state string) Status {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		status := s.Status()
		if !status.Running && status.Task != nil && status.Task.State == state {
			return status
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("did not reach %s: %+v", state, s.Status())
	return Status{}
}
func TestAutomaticPrepareCutoverAndProtectedFinalArchive(t *testing.T) {
	s, repo, transport, input := fixtureService(t)
	ctx := context.Background()
	wrong := input
	wrong.AdministratorPassword = "wrong"
	if _, err := s.Start(ctx, "admin-one", wrong); err == nil {
		t.Fatal("bad administrator password accepted")
	}
	if _, err := s.Start(ctx, "admin-one", input); err != nil {
		t.Fatal(err)
	}
	ready := awaitState(t, s, "ready")
	if ready.Frozen || repo.exports.Load() != 0 {
		t.Fatal("prepare touched source business")
	}
	if _, err := s.Continue(ctx, "admin-one", input.AdministratorPassword, testID, "missing"); err == nil {
		t.Fatal("unconfirmed cutover accepted")
	}
	if _, err := s.Continue(ctx, "admin-one", input.AdministratorPassword, testID, "CUTOVER"); err != nil {
		t.Fatal(err)
	}
	complete := awaitState(t, s, "completed")
	if !complete.Frozen || !complete.CredentialsRequired || repo.exports.Load() != 1 || complete.Task.BackupID == "" {
		t.Fatal("source/backup safety invariant broken")
	}
	raw, err := s.backup.Recovery(complete.Task.BackupID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrationbackup.Open(raw, input.Password, s.config.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(ctx, "admin-one", input); err != nil {
		t.Fatal("idempotent completion rejected", err)
	}
	input.Confirm = "ROLLBACK"
	if _, err := s.Rollback(ctx, "admin-one", input); err == nil {
		t.Fatal("active receiver rollback allowed")
	}
	transport.mu.Lock()
	actions := strings.Join(transport.actions, ",")
	transport.mu.Unlock()
	if actions != "prepare,restore,activate" {
		t.Fatal(actions)
	}
	files, err := os.ReadDir(s.config.Directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		raw, _ := os.ReadFile(filepath.Join(s.config.Directory, file.Name()))
		for _, secret := range []string{input.Credentials.Password, input.Password, input.AdministratorPassword} {
			if bytes.Contains(raw, []byte(secret)) {
				t.Fatal("credential persisted")
			}
		}
	}
}
func TestInterruptedRestoreResumesSameFinalBackupAfterRestart(t *testing.T) {
	s, repo, transport, input := fixtureService(t)
	ctx := context.Background()
	transport.fail = "restore"
	if _, err := s.Start(ctx, "admin-one", input); err != nil {
		t.Fatal(err)
	}
	awaitState(t, s, "ready")
	if _, err := s.Continue(ctx, "admin-one", input.AdministratorPassword, testID, "CUTOVER"); err != nil {
		t.Fatal(err)
	}
	failed := awaitState(t, s, "failed")
	if strings.Contains(failed.Task.Message, "private diagnostic") || failed.Task.BackupID == "" || !failed.Frozen {
		t.Fatal("failure exposed diagnostics or lost isolation")
	}
	s.Close()
	resumed, err := New(s.auth, s.audit, s.backup, s.config)
	if err != nil {
		t.Fatal(err)
	}
	// A real process restart gets a fresh lifetime context, while credentials stay absent.
	resumed.Close()
	config := s.config
	config.Context = context.Background()
	resumed, err = New(s.auth, s.audit, s.backup, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resumed.Close)
	if !resumed.Status().CredentialsRequired || !resumed.Status().Frozen {
		t.Fatal("restart lost durable isolation")
	}
	if _, err := resumed.Start(ctx, "admin-one", input); err != nil {
		t.Fatal(err)
	}
	complete := awaitState(t, resumed, "completed")
	if complete.Task.BackupID != failed.Task.BackupID || repo.exports.Load() != 1 {
		t.Fatal("resume replaced final archive")
	}
}
func TestRollbackStopsReceiverBeforeThawAndPersistsCancellation(t *testing.T) {
	s, _, _, input := fixtureService(t)
	ctx := context.Background()
	if _, err := s.Start(ctx, "admin-one", input); err != nil {
		t.Fatal(err)
	}
	awaitState(t, s, "ready")
	input.Confirm = "ROLLBACK"
	if _, err := s.Rollback(ctx, "admin-one", input); err != nil {
		t.Fatal(err)
	}
	if awaitState(t, s, "cancelled").Frozen {
		t.Fatal("source did not thaw")
	}
	if _, err := s.Rollback(ctx, "admin-one", input); err != nil {
		t.Fatal("repeat rollback rejected")
	}
}
func TestPersistenceFailureDoesNotLeavePhantomRunningTask(t *testing.T) {
	s, _, _, input := fixtureService(t)
	original := s.config.Directory
	s.config.Directory = filepath.Join(original, "missing")
	if _, err := s.Start(context.Background(), "admin-one", input); err == nil {
		t.Fatal("write failure ignored")
	}
	if s.Status().Task != nil || s.Status().Running || !s.Status().CredentialsRequired {
		t.Fatal("phantom task survived failed persistence")
	}
	s.config.Directory = original
	if _, err := s.Start(context.Background(), "admin-one", input); err != nil {
		t.Fatal(err)
	}
	awaitState(t, s, "ready")
	s.config.Directory = filepath.Join(original, "missing")
	if _, err := s.Continue(context.Background(), "admin-one", input.AdministratorPassword, testID, "CUTOVER"); err == nil {
		t.Fatal("failed cutover write ignored")
	}
	if s.Status().Task.State != "ready" || s.Status().Running {
		t.Fatal("cutover stuck running")
	}
}
func TestTargetValidationAndLateReceiverCompletion(t *testing.T) {
	s, _, _, input := fixtureService(t)
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "192.0.2.1", "::1"} {
		input.Target.Host = ip
		if err := s.validate(input); !errors.Is(err, faults.ErrValidation) {
			t.Fatalf("unsafe target accepted: %s", ip)
		}
	}
	if err := writePrivate(filepath.Join(s.config.Directory, "received.json"), Task{ID: testID, State: "completed"}); err != nil {
		t.Fatal(err)
	}
	if status := s.Status(); status.Task == nil || status.Task.State != "completed" {
		t.Fatal("late receiver receipt missing")
	}
	s.Close()
	input.Target.Host = "8.8.8.8"
	if _, err := s.Start(context.Background(), "admin-one", input); err == nil {
		t.Fatal("closed service accepted task")
	}
}
