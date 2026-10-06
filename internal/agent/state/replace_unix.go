//go:build !windows

package state

import "os"

func replaceFile(source, target string) error {
	return os.Rename(source, target)
}
