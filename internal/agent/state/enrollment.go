package state

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/hongle/hl-panel/internal/securetoken"
)

// The attempt is private local recovery material. It contains no raw group
// token and must survive service restarts and failed registration responses.
type enrollmentAttempt struct {
	Origin    string `json:"origin"`
	TokenHash string `json:"token_sha256"`
	Secret    string `json:"secret"`
}

func (s *CredentialStore) enrollmentPath() string {
	return filepath.Join(filepath.Dir(s.path), "enrollment-attempt.json")
}

func (s *CredentialStore) EnrollmentSecret(origin, token string) (string, error) {
	path := s.enrollmentPath()
	var previous enrollmentAttempt
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
			return "", errors.New("enrollment attempt must be a private regular file")
		}
		file, err := os.Open(path)
		if err != nil {
			return "", err
		}
		decoder := json.NewDecoder(io.LimitReader(file, 4096))
		decoder.DisallowUnknownFields()
		decodeErr := decoder.Decode(&previous)
		trailingErr := decoder.Decode(&struct{}{})
		closeErr := file.Close()
		if decodeErr != nil {
			return "", errors.New("invalid enrollment attempt")
		}
		if !errors.Is(trailingErr, io.EOF) {
			return "", errors.New("trailing enrollment attempt data")
		}
		if closeErr != nil {
			return "", closeErr
		}
		decoded, err := hex.DecodeString(previous.Secret)
		if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != previous.Secret {
			return "", errors.New("invalid enrollment attempt secret")
		}
		if previous.Origin != origin {
			return "", errors.New("enrollment attempt belongs to another panel")
		}
		if previous.TokenHash == securetoken.Hash(token) {
			return previous.Secret, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	attempt := enrollmentAttempt{Origin: origin, TokenHash: securetoken.Hash(token), Secret: hex.EncodeToString(secret)}
	data, err := json.Marshal(attempt)
	if err != nil {
		return "", err
	}
	if err := writeFileAtomic(path, append(data, '\n'), 0o600); err != nil {
		return "", err
	}
	return attempt.Secret, nil
}

func (s *CredentialStore) ClearEnrollmentAttempt() error {
	if err := os.Remove(s.enrollmentPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(filepath.Dir(s.path))
}
