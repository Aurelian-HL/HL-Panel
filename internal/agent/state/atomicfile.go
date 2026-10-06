package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

func ensurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create private directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("private data path is not a regular directory")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("protect private directory: %w", err)
	}
	return nil
}

func writeFileAtomic(path string, data []byte, permission os.FileMode) (err error) {
	directory := filepath.Dir(path)
	if err := ensurePrivateDir(directory); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".agent-state-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
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
	if err = os.Chmod(path, permission); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func syncDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
