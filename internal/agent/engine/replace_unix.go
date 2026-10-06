//go:build !windows

package engine

import "os"

func replaceFile(source, target string) error {
	return os.Rename(source, target)
}
