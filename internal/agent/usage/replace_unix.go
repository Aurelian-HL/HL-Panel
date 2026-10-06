//go:build !windows

package usage

import "os"

func replaceFile(source, target string) error { return os.Rename(source, target) }
