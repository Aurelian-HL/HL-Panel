package state

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type AtomicStateStore struct {
	statePath        string
	configurationDir string
	mu               sync.Mutex
}

func NewAtomicStateStore(statePath, configurationDir string) *AtomicStateStore {
	return &AtomicStateStore{
		statePath:        filepath.Clean(statePath),
		configurationDir: filepath.Clean(configurationDir),
	}
}

func (s *AtomicStateStore) Load() (AgentState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadUnlocked()
}

func (s *AtomicStateStore) Begin(desired agentv1.DesiredNodeConfig, now time.Time) (AgentState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	desired, err := normalizeDesired(desired)
	if err != nil {
		return AgentState{}, err
	}
	current, err := s.loadUnlocked()
	if err != nil {
		return AgentState{}, err
	}
	if current.Applied != nil {
		if desired.Generation < current.Applied.Generation {
			return AgentState{}, ErrGenerationRollback
		}
		if desired.Generation == current.Applied.Generation {
			if desired.ConfigSHA256 != current.Applied.ConfigSHA256 {
				return AgentState{}, ErrGenerationConflict
			}
			return AgentState{}, ErrGenerationRollback
		}
	}
	if current.Pending != nil {
		if desired.Generation == current.Pending.Generation {
			if desired.ConfigSHA256 != current.Pending.ConfigSHA256 {
				return AgentState{}, ErrGenerationConflict
			}
			if err := s.writeConfigurationUnlocked(desired); err != nil {
				return AgentState{}, err
			}
			return current, nil
		}
		if desired.Generation < current.Pending.Generation {
			return AgentState{}, ErrGenerationRollback
		}
		return AgentState{}, ErrPendingApplyExists
	}
	if err := s.writeConfigurationUnlocked(desired); err != nil {
		return AgentState{}, err
	}
	current.Pending = &PendingNodeConfig{
		Generation:   desired.Generation,
		Engine:       desired.Engine,
		ConfigSHA256: desired.ConfigSHA256,
		StartedAt:    now.UTC().Format(time.RFC3339Nano),
	}
	current.PendingReceipts = nil
	current.LastApplyPhase = agentv1.ApplyPhasePrepare
	current.LastApplyStatus = ""
	current.LastApplyMessage = "configuration apply pending"
	current.UpdatedAt = now.UTC().Format(time.RFC3339Nano)
	if err := s.saveUnlocked(current); err != nil {
		return AgentState{}, err
	}
	return current, nil
}

func (s *AtomicStateStore) Complete(desired agentv1.DesiredNodeConfig, now time.Time) (AgentState, error) {
	return s.complete(desired, nil, now)
}

func (s *AtomicStateStore) CompleteWithReceipts(desired agentv1.DesiredNodeConfig, receipts []agentv1.ApplyResultRequest, now time.Time) (AgentState, error) {
	if len(receipts) != 2 || receipts[0].Phase != agentv1.ApplyPhaseCommit || receipts[1].Phase != agentv1.ApplyPhaseVerify ||
		receipts[0].Generation != desired.Generation || receipts[0].ConfigSHA256 != desired.ConfigSHA256 {
		return AgentState{}, errors.New("complete requires matching commit and verify receipts")
	}
	if err := validatePendingReceipts(receipts); err != nil {
		return AgentState{}, err
	}
	return s.complete(desired, receipts, now)
}

func (s *AtomicStateStore) complete(desired agentv1.DesiredNodeConfig, receipts []agentv1.ApplyResultRequest, now time.Time) (AgentState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	desired, err := normalizeDesired(desired)
	if err != nil {
		return AgentState{}, err
	}
	current, err := s.loadUnlocked()
	if err != nil {
		return AgentState{}, err
	}
	if current.Pending == nil {
		return AgentState{}, ErrNoPendingApply
	}
	if current.Pending.Generation != desired.Generation || current.Pending.ConfigSHA256 != desired.ConfigSHA256 {
		return AgentState{}, errors.New("pending apply does not match desired node configuration")
	}
	stored, err := s.readConfigurationUnlocked(current.Pending.Generation, current.Pending.Engine, current.Pending.ConfigSHA256)
	if err != nil {
		return AgentState{}, err
	}
	if !bytes.Equal(stored.Config, desired.Config) {
		return AgentState{}, ErrGenerationConflict
	}
	current.Applied = &AppliedNodeConfig{
		Generation:   desired.Generation,
		Engine:       desired.Engine,
		ConfigSHA256: desired.ConfigSHA256,
		AppliedAt:    now.UTC().Format(time.RFC3339Nano),
	}
	current.Pending = nil
	current.PendingReceipts = append([]agentv1.ApplyResultRequest(nil), receipts...)
	current.LastApplyPhase = agentv1.ApplyPhaseVerify
	current.LastApplyStatus = agentv1.ApplyStatusSucceeded
	current.LastApplyMessage = "configuration committed and verified"
	current.UpdatedAt = now.UTC().Format(time.RFC3339Nano)
	if err := s.saveUnlocked(current); err != nil {
		return AgentState{}, err
	}
	return current, nil
}

func (s *AtomicStateStore) RecordResultWithReceipts(phase agentv1.ApplyPhase, status agentv1.ApplyStatus, message string, receipts []agentv1.ApplyResultRequest, now time.Time) (AgentState, error) {
	if len(receipts) != 2 || receipts[0].Phase != agentv1.ApplyPhaseCommit || receipts[1].Phase != agentv1.ApplyPhaseVerify {
		return AgentState{}, errors.New("restore requires commit and verify receipts")
	}
	if err := validatePendingReceipts(receipts); err != nil {
		return AgentState{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.loadUnlocked()
	if err != nil {
		return AgentState{}, err
	}
	if current.Applied == nil || current.Applied.Generation != receipts[0].Generation ||
		current.Applied.ConfigSHA256 != receipts[0].ConfigSHA256 || current.Pending != nil {
		return AgentState{}, errors.New("restored receipts do not match applied configuration")
	}
	current.PendingReceipts = append([]agentv1.ApplyResultRequest(nil), receipts...)
	current.LastApplyPhase = phase
	current.LastApplyStatus = status
	current.LastApplyMessage = sanitizeMessage(message)
	current.UpdatedAt = now.UTC().Format(time.RFC3339Nano)
	if err := s.saveUnlocked(current); err != nil {
		return AgentState{}, err
	}
	return current, nil
}

func (s *AtomicStateStore) Abort(phase agentv1.ApplyPhase, status agentv1.ApplyStatus, message string, now time.Time) (AgentState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.loadUnlocked()
	if err != nil {
		return AgentState{}, err
	}
	current.Pending = nil
	current.LastApplyPhase = phase
	current.LastApplyStatus = status
	current.LastApplyMessage = sanitizeMessage(message)
	current.UpdatedAt = now.UTC().Format(time.RFC3339Nano)
	if err := s.saveUnlocked(current); err != nil {
		return AgentState{}, err
	}
	return current, nil
}

func (s *AtomicStateStore) RecordResult(phase agentv1.ApplyPhase, status agentv1.ApplyStatus, message string, now time.Time) (AgentState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.loadUnlocked()
	if err != nil {
		return AgentState{}, err
	}
	current.LastApplyPhase = phase
	current.LastApplyStatus = status
	current.LastApplyMessage = sanitizeMessage(message)
	current.UpdatedAt = now.UTC().Format(time.RFC3339Nano)
	if err := s.saveUnlocked(current); err != nil {
		return AgentState{}, err
	}
	return current, nil
}

func (s *AtomicStateStore) LastKnownGood() (agentv1.DesiredNodeConfig, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.loadUnlocked()
	if err != nil {
		return agentv1.DesiredNodeConfig{}, false, err
	}
	if current.Applied == nil {
		return agentv1.DesiredNodeConfig{}, false, nil
	}
	desired, err := s.readConfigurationUnlocked(current.Applied.Generation, current.Applied.Engine, current.Applied.ConfigSHA256)
	if err != nil {
		return agentv1.DesiredNodeConfig{}, false, err
	}
	return desired, true, nil
}

func (s *AtomicStateStore) PendingDesired() (agentv1.DesiredNodeConfig, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.loadUnlocked()
	if err != nil {
		return agentv1.DesiredNodeConfig{}, false, err
	}
	if current.Pending == nil {
		return agentv1.DesiredNodeConfig{}, false, nil
	}
	desired, err := s.readConfigurationUnlocked(current.Pending.Generation, current.Pending.Engine, current.Pending.ConfigSHA256)
	if err != nil {
		return agentv1.DesiredNodeConfig{}, false, err
	}
	return desired, true, nil
}

func (s *AtomicStateStore) loadUnlocked() (AgentState, error) {
	file, err := os.Open(s.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return AgentState{}, nil
	}
	if err != nil {
		return AgentState{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 256<<10))
	decoder.DisallowUnknownFields()
	var current AgentState
	if err := decoder.Decode(&current); err != nil {
		return AgentState{}, fmt.Errorf("decode local state: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return AgentState{}, errors.New("local state contains trailing data")
	}
	if current.Applied != nil {
		current.Applied.ConfigSHA256 = strings.ToLower(current.Applied.ConfigSHA256)
	}
	if current.Pending != nil {
		current.Pending.ConfigSHA256 = strings.ToLower(current.Pending.ConfigSHA256)
	}
	if err := current.validate(); err != nil {
		return AgentState{}, err
	}
	return current, nil
}

func (s *AtomicStateStore) saveUnlocked(current AgentState) error {
	if err := current.validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFileAtomic(s.statePath, data, 0o600)
}

func (s *AtomicStateStore) writeConfigurationUnlocked(desired agentv1.DesiredNodeConfig) error {
	path, err := s.configurationPath(desired.Generation, desired.ConfigSHA256)
	if err != nil {
		return err
	}
	if existing, err := readRegularFile(path, MaxConfigurationBytes); err == nil {
		if !bytes.Equal(existing, desired.Config) {
			return ErrGenerationConflict
		}
		return verifyConfigurationHash(existing, desired.ConfigSHA256)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := writeFileAtomic(path, desired.Config, 0o600); err != nil {
		return err
	}
	stored, err := readRegularFile(path, MaxConfigurationBytes)
	if err != nil {
		return err
	}
	if !bytes.Equal(stored, desired.Config) {
		return ErrConfigurationHash
	}
	return verifyConfigurationHash(stored, desired.ConfigSHA256)
}

func (s *AtomicStateStore) readConfigurationUnlocked(generation agentv1.NodeConfigGeneration, engine agentv1.Engine, hash string) (agentv1.DesiredNodeConfig, error) {
	path, err := s.configurationPath(generation, hash)
	if err != nil {
		return agentv1.DesiredNodeConfig{}, err
	}
	configuration, err := readRegularFile(path, MaxConfigurationBytes)
	if err != nil {
		return agentv1.DesiredNodeConfig{}, err
	}
	if err := verifyConfigurationHash(configuration, hash); err != nil {
		return agentv1.DesiredNodeConfig{}, err
	}
	return agentv1.DesiredNodeConfig{
		Generation:   generation,
		Engine:       engine,
		ConfigSHA256: hash,
		Config:       configuration,
	}, nil
}

func (s *AtomicStateStore) configurationPath(generation agentv1.NodeConfigGeneration, hash string) (string, error) {
	normalized, err := normalizeSHA256(hash)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.configurationDir, fmt.Sprintf("%020d-%s.json", uint64(generation), normalized)), nil
}

func normalizeDesired(desired agentv1.DesiredNodeConfig) (agentv1.DesiredNodeConfig, error) {
	if desired.Generation == 0 {
		return agentv1.DesiredNodeConfig{}, errors.New("desired generation must be greater than zero")
	}
	if desired.Engine != agentv1.EngineNodeBundle {
		return agentv1.DesiredNodeConfig{}, fmt.Errorf("unsupported desired engine %q", desired.Engine)
	}
	if len(desired.Config) == 0 || len(desired.Config) > MaxConfigurationBytes {
		return agentv1.DesiredNodeConfig{}, errors.New("desired configuration has an invalid size")
	}
	hash, err := normalizeSHA256(desired.ConfigSHA256)
	if err != nil {
		return agentv1.DesiredNodeConfig{}, err
	}
	if err := verifyConfigurationHash(desired.Config, hash); err != nil {
		return agentv1.DesiredNodeConfig{}, err
	}
	desired.ConfigSHA256 = hash
	desired.Config = bytes.Clone(desired.Config)
	return desired, nil
}

func normalizeSHA256(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != sha256.Size*2 {
		return "", errors.New("SHA-256 must contain 64 hexadecimal characters")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", errors.New("SHA-256 must contain 64 hexadecimal characters")
	}
	return value, nil
}

func verifyConfigurationHash(configuration []byte, expected string) error {
	sum := sha256.Sum256(configuration)
	if hex.EncodeToString(sum[:]) != strings.ToLower(expected) {
		return ErrConfigurationHash
	}
	return nil
}

var ErrConfigurationHash = errors.New("stored configuration SHA-256 mismatch")

func readRegularFile(path string, maximum int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("configuration path is not a regular file")
	}
	if info.Size() > maximum {
		return nil, fmt.Errorf("configuration exceeds %d bytes", maximum)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, fmt.Errorf("configuration exceeds %d bytes", maximum)
	}
	return data, nil
}
