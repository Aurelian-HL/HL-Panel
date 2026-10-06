package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type CredentialStore struct {
	path string
}

func NewCredentialStore(path string) *CredentialStore {
	return &CredentialStore{path: filepath.Clean(path)}
}

func (s *CredentialStore) Prepare() error {
	return ensurePrivateDir(filepath.Dir(s.path))
}

func (s *CredentialStore) Exists() (bool, error) {
	info, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("credential path is not a regular file")
	}
	return true, nil
}

func (s *CredentialStore) Load() (Credentials, error) {
	info, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return Credentials{}, ErrCredentialsNotFound
	}
	if err != nil {
		return Credentials{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Credentials{}, errors.New("credential path is not a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return Credentials{}, errors.New("credential file permissions are broader than 0600")
	}
	file, err := os.Open(s.path)
	if err != nil {
		return Credentials{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 64<<10))
	decoder.DisallowUnknownFields()
	var credentials Credentials
	if err := decoder.Decode(&credentials); err != nil {
		return Credentials{}, fmt.Errorf("decode credential file: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Credentials{}, errors.New("credential file contains trailing data")
	}
	if err := credentials.validate(); err != nil {
		return Credentials{}, err
	}
	return credentials, nil
}

func (s *CredentialStore) SaveOnce(response agentv1.EnrollmentResponse, now time.Time) (err error) {
	if err := validateCredentialField(response.NodeID, "node_id"); err != nil {
		return err
	}
	if err := validateCredentialField(response.NodeCredential, "node_credential"); err != nil {
		return err
	}
	if err := ensurePrivateDir(filepath.Dir(s.path)); err != nil {
		return err
	}
	credentials := Credentials{
		NodeID:         response.NodeID,
		NodeCredential: response.NodeCredential,
		EnrolledAt:     now.UTC().Format(time.RFC3339Nano),
	}
	data, err := json.MarshalIndent(credentials, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	directory := filepath.Dir(s.path)
	file, err := os.CreateTemp(directory, ".credentials-*")
	if err != nil {
		return err
	}
	stagedPath := file.Name()
	defer func() {
		_ = file.Close()
		_ = os.Remove(stagedPath)
	}()
	if err = file.Chmod(0o600); err != nil {
		return err
	}
	if _, err = file.Write(data); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	// A hard link publishes the complete file atomically and fails if another
	// enrollment has already claimed the identity path. Rename would overwrite it.
	if err = os.Link(stagedPath, s.path); errors.Is(err, os.ErrExist) {
		return ErrAlreadyEnrolled
	} else if err != nil {
		return err
	}
	if err = os.Remove(stagedPath); err != nil {
		return err
	}
	return syncDirectory(directory)
}
