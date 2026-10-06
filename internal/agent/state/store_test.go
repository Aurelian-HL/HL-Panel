package state

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestCredentialStoreSavesEnrollmentOnlyOnce(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "private", "credentials.json")
	store := NewCredentialStore(path)
	response := agentv1.EnrollmentResponse{NodeID: "node-1", NodeCredential: "secret-1"}
	if err := store.SaveOnce(response, time.Unix(100, 0)); err != nil {
		t.Fatalf("SaveOnce() error = %v", err)
	}
	if err := store.SaveOnce(agentv1.EnrollmentResponse{NodeID: "node-2", NodeCredential: "secret-2"}, time.Unix(200, 0)); !errors.Is(err, ErrAlreadyEnrolled) {
		t.Fatalf("second SaveOnce() error = %v, want ErrAlreadyEnrolled", err)
	}
	credentials, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if credentials.NodeID != response.NodeID || credentials.NodeCredential != response.NodeCredential {
		t.Fatalf("Load() = %#v, want first enrollment response", credentials)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat(credentials) error = %v", err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("credential permissions = %o, want 600", got)
		}
	}
}

func TestCredentialStoreInterruptedStagingLeavesNoPublishedIdentity(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "private")
	path := filepath.Join(directory, "credentials.json")
	store := NewCredentialStore(path)
	if err := store.Prepare(); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	staged, err := os.CreateTemp(directory, ".credentials-*")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	if _, err := staged.WriteString(`{"node_id":"incomplete"`); err != nil {
		t.Fatalf("write interrupted staging file: %v", err)
	}
	if err := staged.Close(); err != nil {
		t.Fatalf("close interrupted staging file: %v", err)
	}
	if _, err := store.Load(); !errors.Is(err, ErrCredentialsNotFound) {
		t.Fatalf("Load() with interrupted staging file error = %v, want ErrCredentialsNotFound", err)
	}
	response := agentv1.EnrollmentResponse{NodeID: "node-complete", NodeCredential: "secret-complete"}
	if err := store.SaveOnce(response, time.Unix(100, 0)); err != nil {
		t.Fatalf("SaveOnce() after interrupted staging error = %v", err)
	}
	got, err := store.Load()
	if err != nil || got.NodeID != response.NodeID || got.NodeCredential != response.NodeCredential {
		t.Fatalf("Load() after SaveOnce() = (%#v, %v)", got, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".credentials-") && entry.Name() != filepath.Base(staged.Name()) {
			t.Fatalf("SaveOnce() left a staging file: %s", entry.Name())
		}
	}
}

func TestCredentialStoreConcurrentSaveNeverOverwritesIdentity(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "private", "credentials.json")
	const writers = 12
	results := make(chan error, writers)
	var wait sync.WaitGroup
	for i := 0; i < writers; i++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			store := NewCredentialStore(path)
			results <- store.SaveOnce(agentv1.EnrollmentResponse{
				NodeID: "node-" + string(rune('a'+index)), NodeCredential: "secret-" + string(rune('a'+index)),
			}, time.Unix(int64(index+1), 0))
		}(i)
	}
	wait.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrAlreadyEnrolled) {
			t.Fatalf("concurrent SaveOnce() error = %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful SaveOnce() calls = %d, want 1", successes)
	}
	store := NewCredentialStore(path)
	credentials, err := store.Load()
	if err != nil || credentials.NodeID == "" || credentials.NodeCredential == "" {
		t.Fatalf("Load() after concurrent SaveOnce() = (%#v, %v)", credentials, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Fatalf("concurrent SaveOnce() left staging files: %#v", entries)
	}
}

func TestAtomicStateStorePersistsAndVerifiesConfiguration(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	store := newTestStore(directory)
	desired := testDesired(1, `{"schema_version":1,"fragments":[]}`)
	if _, err := store.Begin(desired, time.Unix(100, 0)); err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	pending, ok, err := store.PendingDesired()
	if err != nil || !ok {
		t.Fatalf("PendingDesired() = (%#v, %v, %v), want pending configuration", pending, ok, err)
	}
	if string(pending.Config) != string(desired.Config) {
		t.Fatalf("pending config differs from desired")
	}
	if _, err := store.Complete(desired, time.Unix(101, 0)); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	lastKnownGood, ok, err := store.LastKnownGood()
	if err != nil || !ok {
		t.Fatalf("LastKnownGood() = (%#v, %v, %v)", lastKnownGood, ok, err)
	}
	if lastKnownGood.Generation != desired.Generation || lastKnownGood.ConfigSHA256 != desired.ConfigSHA256 {
		t.Fatalf("LastKnownGood() = %#v, want generation/hash from desired", lastKnownGood)
	}

	path, err := store.configurationPath(desired.Generation, desired.ConfigSHA256)
	if err != nil {
		t.Fatalf("configurationPath() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"fragments":[{"tampered":true}]}`), 0o600); err != nil {
		t.Fatalf("WriteFile(tamper) error = %v", err)
	}
	if _, _, err := store.LastKnownGood(); !errors.Is(err, ErrConfigurationHash) {
		t.Fatalf("LastKnownGood() after tamper error = %v, want ErrConfigurationHash", err)
	}
}

func TestAtomicStateStoreRejectsGenerationRollbackAndConflict(t *testing.T) {
	t.Parallel()
	store := newTestStore(t.TempDir())
	applied := testDesired(2, `{"schema_version":1,"fragments":[]}`)
	if _, err := store.Begin(applied, time.Unix(100, 0)); err != nil {
		t.Fatalf("Begin(applied) error = %v", err)
	}
	if _, err := store.Complete(applied, time.Unix(101, 0)); err != nil {
		t.Fatalf("Complete(applied) error = %v", err)
	}
	if _, err := store.Begin(testDesired(1, `{"schema_version":1,"fragments":[]}`), time.Unix(102, 0)); !errors.Is(err, ErrGenerationRollback) {
		t.Fatalf("Begin(lower generation) error = %v, want ErrGenerationRollback", err)
	}
	if _, err := store.Begin(testDesired(2, `{"schema_version":1,"fragments":[{}]}`), time.Unix(103, 0)); !errors.Is(err, ErrGenerationConflict) {
		t.Fatalf("Begin(conflicting generation) error = %v, want ErrGenerationConflict", err)
	}
}

func TestPendingJournalSurvivesRestartWithoutReplacingLastKnownGood(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	firstStore := newTestStore(directory)
	first := testDesired(1, `{"schema_version":1,"fragments":[]}`)
	if _, err := firstStore.Begin(first, time.Unix(100, 0)); err != nil {
		t.Fatalf("Begin(first) error = %v", err)
	}
	if _, err := firstStore.Complete(first, time.Unix(101, 0)); err != nil {
		t.Fatalf("Complete(first) error = %v", err)
	}
	second := testDesired(2, `{"schema_version":1,"fragments":[{"group_id":"next"}]}`)
	if _, err := firstStore.Begin(second, time.Unix(102, 0)); err != nil {
		t.Fatalf("Begin(second) error = %v", err)
	}

	restarted := newTestStore(directory)
	pending, ok, err := restarted.PendingDesired()
	if err != nil || !ok || pending.Generation != 2 {
		t.Fatalf("PendingDesired() after restart = (%#v, %v, %v)", pending, ok, err)
	}
	lastKnownGood, ok, err := restarted.LastKnownGood()
	if err != nil || !ok || lastKnownGood.Generation != 1 {
		t.Fatalf("LastKnownGood() after restart = (%#v, %v, %v)", lastKnownGood, ok, err)
	}
	if _, err := restarted.Abort(agentv1.ApplyPhaseRollback, agentv1.ApplyStatusRolledBack, "recovered after restart", time.Unix(103, 0)); err != nil {
		t.Fatalf("Abort() error = %v", err)
	}
	if _, ok, err := restarted.PendingDesired(); err != nil || ok {
		t.Fatalf("PendingDesired() after Abort = (_, %v, %v), want no pending", ok, err)
	}
}

func TestPendingGenerationRewritesMissingConfigurationOnResume(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	store := newTestStore(directory)
	desired := testDesired(1, `{"schema_version":1,"fragments":[]}`)
	if _, err := store.Begin(desired, time.Unix(100, 0)); err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	path, err := store.configurationPath(desired.Generation, desired.ConfigSHA256)
	if err != nil {
		t.Fatalf("configurationPath() error = %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove(configuration) error = %v", err)
	}
	if _, err := store.Begin(desired, time.Unix(101, 0)); err != nil {
		t.Fatalf("Begin(resume) error = %v, want rewrite and resume", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Stat(rewritten configuration) error = %v", err)
	}
}

func newTestStore(directory string) *AtomicStateStore {
	return NewAtomicStateStore(
		filepath.Join(directory, "state.json"),
		filepath.Join(directory, "configurations"),
	)
}

func testDesired(generation agentv1.NodeConfigGeneration, configuration string) agentv1.DesiredNodeConfig {
	config := []byte(configuration)
	sum := sha256.Sum256(config)
	return agentv1.DesiredNodeConfig{
		Generation:   generation,
		Engine:       agentv1.EngineNodeBundle,
		ConfigSHA256: hex.EncodeToString(sum[:]),
		Config:       config,
	}
}
