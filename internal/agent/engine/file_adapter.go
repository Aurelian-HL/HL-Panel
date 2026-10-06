package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// DryRunFileAdapter exercises the complete activation lifecycle without
// starting or reloading an external engine process.
type DryRunFileAdapter struct {
	stagingPath string
	activePath  string
	adapterKey  string
}

func NewDryRunFileAdapter(stagingPath, activePath string) (*DryRunFileAdapter, error) {
	if !filepath.IsAbs(stagingPath) || !filepath.IsAbs(activePath) {
		return nil, errors.New("engine staging and active paths must be absolute")
	}
	stagingPath = filepath.Clean(stagingPath)
	activePath = filepath.Clean(activePath)
	if stagingPath == activePath {
		return nil, errors.New("engine staging and active paths must be different")
	}
	return &DryRunFileAdapter{
		stagingPath: stagingPath,
		activePath:  activePath,
		adapterKey:  activePath,
	}, nil
}

func (a *DryRunFileAdapter) Prepare(ctx context.Context, desired agentv1.DesiredNodeConfig) (PreparedConfiguration, error) {
	if err := ctx.Err(); err != nil {
		return PreparedConfiguration{}, err
	}
	prepared, err := parseDesired(desired)
	if err != nil {
		return PreparedConfiguration{}, err
	}
	if err := writeFileAtomic(a.stagingPath, prepared.canonicalConfig, 0o600); err != nil {
		return PreparedConfiguration{}, fmt.Errorf("stage engine configuration: %w", err)
	}
	prepared.adapterPath = a.adapterKey
	return prepared, nil
}

func (a *DryRunFileAdapter) Validate(ctx context.Context, prepared PreparedConfiguration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.checkOwner(prepared); err != nil {
		return err
	}
	if err := verifyFile(a.stagingPath, prepared); err != nil {
		return fmt.Errorf("validate staged engine configuration: %w", err)
	}
	return nil
}

func (a *DryRunFileAdapter) Commit(ctx context.Context, prepared PreparedConfiguration) error {
	if err := a.Validate(ctx, prepared); err != nil {
		return err
	}
	if err := writeFileAtomic(a.activePath, prepared.canonicalConfig, 0o600); err != nil {
		return fmt.Errorf("activate engine configuration: %w", err)
	}
	return nil
}

func (a *DryRunFileAdapter) Verify(ctx context.Context, prepared PreparedConfiguration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.checkOwner(prepared); err != nil {
		return err
	}
	if err := verifyFile(a.activePath, prepared); err != nil {
		return fmt.Errorf("verify active engine configuration: %w", err)
	}
	return nil
}

func (a *DryRunFileAdapter) Rollback(ctx context.Context, previous *PreparedConfiguration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if previous == nil {
		if err := removeRegularFile(a.activePath); err != nil {
			return fmt.Errorf("remove unverified active configuration: %w", err)
		}
		return nil
	}
	if err := a.checkOwner(*previous); err != nil {
		return err
	}
	if _, err := parseDesired(previous.DesiredNodeConfig()); err != nil {
		return fmt.Errorf("validate rollback configuration: %w", err)
	}
	if err := writeFileAtomic(a.activePath, previous.canonicalConfig, 0o600); err != nil {
		return fmt.Errorf("restore previous engine configuration: %w", err)
	}
	if err := verifyFile(a.activePath, *previous); err != nil {
		return fmt.Errorf("verify restored engine configuration: %w", err)
	}
	return nil
}

func (a *DryRunFileAdapter) checkOwner(prepared PreparedConfiguration) error {
	if prepared.adapterPath != a.adapterKey {
		return ErrPreparedForOtherEngine
	}
	return nil
}

func verifyFile(path string, expected PreparedConfiguration) error {
	data, err := readRegularFile(path, MaxConfigurationBytes)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, expected.canonicalConfig) {
		return ErrConfigurationHash
	}
	actual, err := parseDesired(expected.DesiredNodeConfigWithConfig(data))
	if err != nil {
		return err
	}
	if actual.ConfigSHA256 != expected.ConfigSHA256 {
		return ErrConfigurationHash
	}
	return nil
}

func (p PreparedConfiguration) DesiredNodeConfigWithConfig(config []byte) agentv1.DesiredNodeConfig {
	return agentv1.DesiredNodeConfig{
		Generation:   p.Generation,
		Engine:       p.Engine,
		ConfigSHA256: p.ConfigSHA256,
		Config:       bytes.Clone(config),
	}
}

func readRegularFile(path string, maximum int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("engine configuration path is not a regular file")
	}
	if info.Size() > maximum {
		return nil, fmt.Errorf("engine configuration exceeds %d bytes", maximum)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, fmt.Errorf("engine configuration exceeds %d bytes", maximum)
	}
	return data, nil
}

func removeRegularFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("engine configuration path is not a regular file")
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}
