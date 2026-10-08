package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
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
)

type migrationRepository struct {
	state         migrationbackup.State
	reads, writes int
}

func (r *migrationRepository) ExportMigration(context.Context) (migrationbackup.State, error) {
	r.reads++
	return r.state, nil
}
func (r *migrationRepository) RestoreMigration(_ context.Context, input migrationbackup.RestoreInput) (migrationbackup.Result, error) {
	id, err := input.SaveRecovery(r.state)
	if err != nil {
		return migrationbackup.Result{}, err
	}
	r.writes++
	return migrationbackup.Result{RecoveryID: id, RestoredAt: time.Now()}, nil
}
func TestMigrationHTTPAuthorizationPreviewAndOverwriteGuard(t *testing.T) {
	hash, err := auth.HashPassword("isolated-admin")
	if err != nil {
		t.Fatal(err)
	}
	store := memoryrepo.New(auth.Administrator{ID: "admin-one", Username: "admin", PasswordHash: hash, CreatedAt: time.Now()})
	snapshot, err := store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	repository := &migrationRepository{state: migrationbackup.State{Snapshot: snapshot, Usage: map[string]json.RawMessage{}}}
	for _, name := range migrationbackup.Tables() {
		repository.state.Usage[name] = json.RawMessage("[]")
	}
	authService := auth.NewService(store, audit.NewService(store), time.Now, time.Hour)
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	service := migrationbackup.New(repository, authService, audit.NewService(store), "v0.1.47", directory, func() migrationbackup.RuntimeSecrets {
		return migrationbackup.RuntimeSecrets{PasswordFingerprintKey: bytes.Repeat([]byte{1}, 32)}
	}, nil)
	handler := httpapi.New(authService, enrollment.NewService(store, time.Now, time.Hour), nodes.NewService(store, time.Now, time.Minute), groups.NewService(store, time.Now), endpoints.NewService(store, time.Now), generations.NewService(store, time.Now), slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.WithMigrationBackup(service))
	for _, path := range []string{"export", "preview", "import"} {
		requestJSON(t, handler, "POST", "/api/v1/panel/migration/"+path, "", nil, 401)
	}
	if repository.reads != 0 || repository.writes != 0 {
		t.Fatal("unauthorized migration reached storage")
	}
	login, err := authService.Login(context.Background(), "admin", "isolated-admin")
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"administrator_password": "wrong", "password": "isolated-backup", "source_url": "https://panel.example.com"}
	requestJSON(t, handler, "POST", "/api/v1/panel/migration/export", login.AccessToken, payload, 400)
	payload["administrator_password"] = "isolated-admin"
	raw := requestJSON(t, handler, "POST", "/api/v1/panel/migration/export", login.AccessToken, payload, 200)
	upload := func(path, confirm, digest string, expected int) []byte {
		t.Helper()
		var buffer bytes.Buffer
		form := multipart.NewWriter(&buffer)
		f, e := form.CreateFormFile("file", "panel.hlbackup")
		if e != nil {
			t.Fatal(e)
		}
		f.Write(raw)
		for key, value := range map[string]string{"administrator_password": "isolated-admin", "password": "isolated-backup", "confirm": confirm, "digest": digest, "target_url": "https://target.example.com"} {
			form.WriteField(key, value)
		}
		form.Close()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/panel/migration/"+path, &buffer)
		req.Header.Set("Content-Type", form.FormDataContentType())
		req.Header.Set("Authorization", "Bearer "+login.AccessToken)
		req.Header.Set("Idempotency-Key", "isolated-import-key")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != expected {
			t.Fatalf("migration %s: code %d", path, response.Code)
		}
		return response.Body.Bytes()
	}
	var preview migrationbackup.Preview
	if json.Unmarshal(upload("preview", "", "", 200), &preview) != nil {
		t.Fatal("invalid preview")
	}
	if preview.Counts["administrators"] != 1 || repository.writes != 0 {
		t.Fatal("preview wrote data")
	}
	upload("import", "", preview.Digest, 400)
	upload("import", "RESTORE", "wrong-digest", 400)
	if repository.writes != 0 {
		t.Fatal("missing confirmation allowed overwrite")
	}
	var result migrationbackup.Result
	if json.Unmarshal(upload("import", "RESTORE", preview.Digest, 200), &result) != nil {
		t.Fatal("invalid receipt")
	}
	if repository.writes != 1 || result.RecoveryID == "" {
		t.Fatal("restore/recovery not performed")
	}
	requestJSON(t, handler, "GET", "/api/v1/panel/migration/recovery/"+result.RecoveryID, "", nil, 401)
	requestJSON(t, handler, "GET", "/api/v1/panel/migration/recovery/"+result.RecoveryID, login.AccessToken, nil, 200)
	requestJSON(t, handler, "GET", "/api/v1/panel/migration/recovery/invalid", login.AccessToken, nil, 404)
}
