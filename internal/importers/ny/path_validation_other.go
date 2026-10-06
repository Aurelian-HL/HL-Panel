//go:build !windows

package ny

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func validatePlatformLocalSnapshotPath(cleaned string) (string, error) {
	return validateOtherLocalPath(cleaned, false)
}

func validatePlatformNewLocalFilePath(cleaned string) (string, error) {
	return validateOtherLocalPath(cleaned, true)
}

func validateOtherLocalPath(cleaned string, allowMissingFinal bool) (string, error) {
	current := string(os.PathSeparator)
	remainder := strings.TrimPrefix(cleaned, current)
	components := strings.Split(remainder, string(os.PathSeparator))
	for index, component := range components {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			if allowMissingFinal && index == len(components)-1 && os.IsNotExist(err) {
				return cleaned, nil
			}
			return "", fmt.Errorf("inspect local path component: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", ErrSnapshotPathReparse
		}
	}
	return cleaned, nil
}
