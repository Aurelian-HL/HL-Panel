package ny

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

var (
	ErrSnapshotPathRequired = errors.New("NY snapshot path is required")
	ErrSnapshotPathAbsolute = errors.New("NY snapshot path must be absolute")
	ErrSnapshotPathRemote   = errors.New("remote and UNC snapshot paths are not allowed")
	ErrSnapshotPathDevice   = errors.New("device and namespaced snapshot paths are not allowed")
	ErrSnapshotPathReparse  = errors.New("snapshot path must not contain symlinks or reparse points")
)

func validateLocalSnapshotPath(input string) (string, error) {
	cleaned, err := cleanLocalAbsolutePath(input)
	if err != nil {
		return "", err
	}
	validated, err := validatePlatformLocalSnapshotPath(cleaned)
	if err != nil {
		return "", fmt.Errorf("validate local NY snapshot path: %w", err)
	}
	return validated, nil
}

// ValidateNewLocalFilePath validates a not-yet-created local report path. The
// final component may be absent, but every parent must already exist and no
// component may cross a symlink, reparse point, network share, or device path.
func ValidateNewLocalFilePath(input string) (string, error) {
	cleaned, err := cleanLocalAbsolutePath(strings.TrimSpace(input))
	if err != nil {
		return "", err
	}
	validated, err := validatePlatformNewLocalFilePath(cleaned)
	if err != nil {
		return "", fmt.Errorf("validate local report path: %w", err)
	}
	return validated, nil
}

func cleanLocalAbsolutePath(input string) (string, error) {
	if input == "" {
		return "", ErrSnapshotPathRequired
	}

	// Reject Windows network and device namespaces before filepath.Clean,
	// filepath.Abs, Lstat, or Open can resolve them. This check is deliberately
	// platform-neutral so a snapshot path cannot become dangerous when moved
	// between development hosts.
	windowsForm := strings.ReplaceAll(input, "/", "\\")
	lowerWindowsForm := strings.ToLower(windowsForm)
	if strings.HasPrefix(windowsForm, `\\`) {
		if strings.HasPrefix(lowerWindowsForm, `\\.\`) || strings.HasPrefix(lowerWindowsForm, `\\?\`) {
			return "", ErrSnapshotPathDevice
		}
		return "", ErrSnapshotPathRemote
	}
	if strings.HasPrefix(lowerWindowsForm, `\??\`) || strings.HasPrefix(lowerWindowsForm, `\device\`) {
		return "", ErrSnapshotPathDevice
	}

	cleaned := filepath.Clean(input)
	if !filepath.IsAbs(cleaned) {
		return "", ErrSnapshotPathAbsolute
	}
	return cleaned, nil
}
