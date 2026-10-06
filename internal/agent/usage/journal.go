package usage

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

const (
	journalVersion  = 1
	maxJournalBytes = 8 << 20
	maxPendingItems = 10_000
)

type Journal struct {
	Version         int                   `json:"version"`
	BootID          string                `json:"boot_id"`
	NextSequence    int64                 `json:"next_sequence"`
	LastCollectedAt time.Time             `json:"last_collected_at"`
	Pending         []agentv1.UsageReport `json:"pending"`
	UpdatedAt       time.Time             `json:"updated_at"`
}

type JournalStore struct {
	path string
	mu   sync.Mutex
}

func NewJournalStore(path string) *JournalStore {
	return &JournalStore{path: filepath.Clean(path)}
}

func (store *JournalStore) Prepare(now time.Time) (Journal, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	journal, err := store.loadUnlocked()
	if err == nil {
		return journal, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Journal{}, err
	}
	bootID, err := newBootID()
	if err != nil {
		return Journal{}, err
	}
	journal = Journal{Version: journalVersion, BootID: bootID, NextSequence: 1, LastCollectedAt: now.UTC(), Pending: []agentv1.UsageReport{}, UpdatedAt: now.UTC()}
	if err := store.saveUnlocked(journal); err != nil {
		return Journal{}, err
	}
	return journal, nil
}

func (store *JournalStore) Load() (Journal, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.loadUnlocked()
}

func (store *JournalStore) update(mutator func(*Journal) error) (Journal, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	journal, err := store.loadUnlocked()
	if err != nil {
		return Journal{}, err
	}
	if err := mutator(&journal); err != nil {
		return Journal{}, err
	}
	if err := validateJournal(journal); err != nil {
		return Journal{}, err
	}
	if err := store.saveUnlocked(journal); err != nil {
		return Journal{}, err
	}
	return journal, nil
}

func (store *JournalStore) loadUnlocked() (Journal, error) {
	info, err := os.Lstat(store.path)
	if err != nil {
		return Journal{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Journal{}, errors.New("usage journal is not a regular file")
	}
	if info.Size() < 1 || info.Size() > maxJournalBytes {
		return Journal{}, errors.New("usage journal size is invalid")
	}
	file, err := os.Open(store.path)
	if err != nil {
		return Journal{}, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxJournalBytes+1))
	if err != nil {
		return Journal{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var journal Journal
	if err := decoder.Decode(&journal); err != nil {
		return Journal{}, fmt.Errorf("decode usage journal: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Journal{}, errors.New("usage journal contains trailing data")
	}
	if err := validateJournal(journal); err != nil {
		return Journal{}, err
	}
	return journal, nil
}

func (store *JournalStore) saveUnlocked(journal Journal) error {
	if err := validateJournal(journal); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if len(raw) > maxJournalBytes {
		return errors.New("usage journal exceeds size limit")
	}
	return writeFileAtomic(store.path, raw, 0o600)
}

func validateJournal(journal Journal) error {
	if journal.Version != journalVersion {
		return fmt.Errorf("unsupported usage journal version %d", journal.Version)
	}
	bootID, err := hex.DecodeString(journal.BootID)
	if err != nil || len(bootID) != 16 {
		return errors.New("usage journal boot_id is invalid")
	}
	if journal.NextSequence < 1 || journal.LastCollectedAt.IsZero() || journal.UpdatedAt.IsZero() {
		return errors.New("usage journal cursor is invalid")
	}
	if len(journal.Pending) > maxPendingItems {
		return errors.New("usage journal pending queue exceeds limit")
	}
	for index, report := range journal.Pending {
		if report.BootID != journal.BootID || report.Sequence < 1 || report.Sequence >= journal.NextSequence {
			return errors.New("usage journal pending report cursor is invalid")
		}
		if index > 0 && report.Sequence != journal.Pending[index-1].Sequence+1 {
			return errors.New("usage journal pending sequence is not contiguous")
		}
	}
	return nil
}

func newBootID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate usage boot id: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func writeFileAtomic(path string, data []byte, permission os.FileMode) (err error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("usage journal directory is not a regular directory")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".usage-journal-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if err != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err = temporary.Chmod(permission); err != nil {
		return err
	}
	if _, err = temporary.Write(data); err != nil {
		return err
	}
	if err = temporary.Sync(); err != nil {
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	if err = replaceFile(temporaryPath, path); err != nil {
		return err
	}
	return os.Chmod(path, permission)
}
