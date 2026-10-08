package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/httpapi"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/migrationbackup"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/control/panelmigration"
)

type automaticTransport struct{ calls atomic.Int32 }

func (*automaticTransport) Probe(context.Context, panelmigration.Target) (string, error) {
	return "SHA256:" + strings.Repeat("a", 43), nil
}
func (s *automaticTransport) Run(context.Context, panelmigration.Target, panelmigration.Credentials, panelmigration.RemoteInput) (panelmigration.RemoteResult, error) {
	s.calls.Add(1)
	return panelmigration.RemoteResult{State: "prepared"}, nil
}

func TestAutomaticMigrationHTTPAuthorizationAndNoSecretPersistence(t *testing.T) {
	hash, err := auth.HashPassword("isolated-admin")
	if err != nil {
		t.Fatal(err)
	}
	store := memoryrepo.New(auth.Administrator{ID: "admin-one", Username: "admin", PasswordHash: hash, CreatedAt: time.Now()})
	events := audit.NewService(store)
	a := auth.NewService(store, events, time.Now, time.Hour)
	directory := t.TempDir()
	os.Chmod(directory, 0700)
	fence, err := panelmigration.OpenFence(directory)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := store.EncodeSnapshot()
	repo := &migrationRepository{state: migrationbackup.State{Snapshot: snapshot, Usage: map[string]json.RawMessage{}}}
	for _, table := range migrationbackup.Tables() {
		repo.state.Usage[table] = json.RawMessage("[]")
	}
	backup := migrationbackup.New(repo, a, events, "v0.1.50", t.TempDir(), func() migrationbackup.RuntimeSecrets {
		return migrationbackup.RuntimeSecrets{PasswordFingerprintKey: bytes.Repeat([]byte{1}, 32)}
	}, nil)
	transport := &automaticTransport{}
	service, err := panelmigration.New(a, events, backup, panelmigration.Config{Directory: directory, Domain: "panel.example.com", SourceIPURL: "https://1.1.1.1", Version: "v0.1.50", Installer: "--migration-receiver", Fence: fence, Transport: transport, Freeze: func(id string) error { return fence.Set(panelmigration.FenceState{Role: "source", ID: id}) }, Thaw: fence.Clear})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	handler := httpapi.New(a, enrollment.NewService(store, time.Now, time.Hour), nodes.NewService(store, time.Now, time.Minute), groups.NewService(store, time.Now), endpoints.NewService(store, time.Now), generations.NewService(store, time.Now), slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.WithAutomaticMigration(service))
	base := "/api/v1/panel/migration/auto"
	requestJSON(t, handler, "GET", base, "", nil, 401)
	for _, path := range []string{"", "/probe", "/continue", "/rollback"} {
		requestJSON(t, handler, "POST", base+path, "", nil, 401)
	}
	login, err := a.Login(context.Background(), "admin", "isolated-admin")
	if err != nil {
		t.Fatal(err)
	}
	input := panelmigration.Input{ID: "12345678-1234-1234-1234-123456789abc", Target: panelmigration.Target{Host: "8.8.8.8", Port: 22, Fingerprint: "SHA256:" + strings.Repeat("a", 43)}, Credentials: panelmigration.Credentials{Password: "test-ssh-secret"}, AdministratorPassword: "wrong", Password: "test-backup-secret", SourceURL: "https://panel.example.com", Confirm: "PREPARE"}
	requestJSON(t, handler, "POST", base, login.AccessToken, input, 400)
	requestJSON(t, handler, "POST", base+"/probe", login.AccessToken, map[string]any{"target": input.Target, "administrator_password": "wrong"}, 400)
	if transport.calls.Load() != 0 || repo.reads != 0 {
		t.Fatal("wrong password reached migration executor")
	}
	input.AdministratorPassword = "isolated-admin"
	bad := input
	bad.Target.Host = "127.0.0.1"
	requestJSON(t, handler, "POST", base, login.AccessToken, bad, 400)
	requestJSON(t, handler, "POST", base, login.AccessToken, input, 202)
	deadline := time.Now().Add(5 * time.Second)
	for service.Status().Running && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if service.Status().Task.State != "ready" {
		t.Fatal("prepare did not finish")
	}
	requestJSON(t, handler, "POST", base, login.AccessToken, input, 202)
	for service.Status().Running && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	response := requestJSON(t, handler, "GET", base, login.AccessToken, nil, 200)
	persisted, err := os.ReadFile(filepath.Join(directory, "task.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{input.AdministratorPassword, input.Password, input.Credentials.Password} {
		if bytes.Contains(response, []byte(secret)) || bytes.Contains(persisted, []byte(secret)) {
			t.Fatal("migration secret exposed")
		}
	}
	requestJSON(t, handler, "POST", base+"/continue", login.AccessToken, map[string]string{"id": input.ID, "administrator_password": input.AdministratorPassword}, 400)
	if repo.reads != 0 || fence.Active() {
		t.Fatal("missing cutover confirmation froze or exported the source")
	}
}
