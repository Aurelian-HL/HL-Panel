//go:build windows

package ny

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

const (
	driveUnknown   = 0
	driveNoRootDir = 1
	driveRemote    = 4
)

var getDriveTypeW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDriveTypeW")

func validatePlatformLocalSnapshotPath(cleaned string) (string, error) {
	return validateWindowsLocalPath(cleaned, false)
}

func validatePlatformNewLocalFilePath(cleaned string) (string, error) {
	return validateWindowsLocalPath(cleaned, true)
}

func validateWindowsLocalPath(cleaned string, allowMissingFinal bool) (string, error) {
	volume := filepath.VolumeName(cleaned)
	if len(volume) != 2 || volume[1] != ':' || !isASCIIAlpha(volume[0]) {
		return "", ErrSnapshotPathDevice
	}
	root := volume + `\`
	driveType, err := windowsDriveType(root)
	if err != nil {
		return "", err
	}
	if driveType == driveRemote {
		return "", ErrSnapshotPathRemote
	}
	if driveType == driveUnknown || driveType == driveNoRootDir {
		return "", ErrSnapshotPathDevice
	}

	remainder := strings.TrimPrefix(cleaned[len(volume):], `\`)
	current := root
	components := strings.Split(remainder, `\`)
	for index, component := range components {
		if component == "" {
			continue
		}
		if unsafeWindowsComponent(component) {
			return "", ErrSnapshotPathDevice
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
		pointer, err := syscall.UTF16PtrFromString(current)
		if err != nil {
			return "", ErrSnapshotPathDevice
		}
		attributes, err := syscall.GetFileAttributes(pointer)
		if err != nil {
			return "", fmt.Errorf("inspect local path attributes: %w", err)
		}
		if attributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return "", ErrSnapshotPathReparse
		}
	}
	return cleaned, nil
}

func windowsDriveType(root string) (uint32, error) {
	pointer, err := syscall.UTF16PtrFromString(root)
	if err != nil {
		return driveUnknown, err
	}
	result, _, _ := getDriveTypeW.Call(uintptr(unsafe.Pointer(pointer)))
	return uint32(result), nil
}

func unsafeWindowsComponent(component string) bool {
	if strings.Contains(component, ":") || strings.ContainsRune(component, '\x00') {
		return true
	}
	trimmed := strings.TrimRight(component, " .")
	if trimmed == "" {
		return true
	}
	base := trimmed
	if index := strings.IndexByte(base, '.'); index >= 0 {
		base = base[:index]
	}
	base = strings.ToUpper(base)
	switch base {
	case "CON", "PRN", "AUX", "NUL", "CLOCK$":
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9'
}

func isASCIIAlpha(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z')
}
