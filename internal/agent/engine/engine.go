package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

const (
	BundleSchemaVersion   = 1
	MaxConfigurationBytes = 4 << 20
)

var (
	ErrUnsupportedEngine      = errors.New("unsupported engine")
	ErrInvalidConfiguration   = errors.New("invalid configuration")
	ErrConfigurationHash      = errors.New("configuration hash mismatch")
	ErrPreparedForOtherEngine = errors.New("configuration was prepared by another engine adapter")
)

// EngineAdapter is the bounded interface between reconciliation and a local
// data-plane engine. The first implementation only atomically activates files.
type EngineAdapter interface {
	Prepare(context.Context, agentv1.DesiredNodeConfig) (PreparedConfiguration, error)
	Validate(context.Context, PreparedConfiguration) error
	Commit(context.Context, PreparedConfiguration) error
	Verify(context.Context, PreparedConfiguration) error
	Rollback(context.Context, *PreparedConfiguration) error
}

type PreparedConfiguration struct {
	Generation   agentv1.NodeConfigGeneration
	Engine       agentv1.Engine
	ConfigSHA256 string
	Bundle       agentv1.ConfigurationBundle

	canonicalConfig []byte
	adapterPath     string
	components      map[agentv1.Engine]PreparedConfiguration
}

func (p PreparedConfiguration) CanonicalConfig() []byte {
	return bytes.Clone(p.canonicalConfig)
}

func (p PreparedConfiguration) DesiredNodeConfig() agentv1.DesiredNodeConfig {
	return agentv1.DesiredNodeConfig{
		Generation:   p.Generation,
		Engine:       p.Engine,
		ConfigSHA256: p.ConfigSHA256,
		Config:       bytes.Clone(p.canonicalConfig),
	}
}

func parseDesired(desired agentv1.DesiredNodeConfig) (PreparedConfiguration, error) {
	if desired.Generation == 0 {
		return PreparedConfiguration{}, fmt.Errorf("%w: generation must be greater than zero", ErrInvalidConfiguration)
	}
	if desired.Engine != agentv1.EngineNodeBundle {
		return PreparedConfiguration{}, fmt.Errorf("%w: %q", ErrUnsupportedEngine, desired.Engine)
	}
	if len(desired.Config) == 0 {
		return PreparedConfiguration{}, fmt.Errorf("%w: node bundle is empty", ErrInvalidConfiguration)
	}
	if len(desired.Config) > MaxConfigurationBytes {
		return PreparedConfiguration{}, fmt.Errorf("%w: node bundle exceeds %d bytes", ErrInvalidConfiguration, MaxConfigurationBytes)
	}
	wantHash, err := normalizeSHA256(desired.ConfigSHA256)
	if err != nil {
		return PreparedConfiguration{}, err
	}
	actualHash := sha256.Sum256(desired.Config)
	if hex.EncodeToString(actualHash[:]) != wantHash {
		return PreparedConfiguration{}, ErrConfigurationHash
	}

	var bundle agentv1.ConfigurationBundle
	if err := decodeStrict(desired.Config, &bundle); err != nil {
		return PreparedConfiguration{}, fmt.Errorf("%w: decode node bundle: %v", ErrInvalidConfiguration, err)
	}
	if err := validateBundle(bundle); err != nil {
		return PreparedConfiguration{}, err
	}

	return PreparedConfiguration{
		Generation:      desired.Generation,
		Engine:          desired.Engine,
		ConfigSHA256:    wantHash,
		Bundle:          bundle,
		canonicalConfig: bytes.Clone(desired.Config),
	}, nil
}

func validateBundle(bundle agentv1.ConfigurationBundle) error {
	if bundle.SchemaVersion != BundleSchemaVersion {
		return fmt.Errorf("%w: unsupported bundle schema_version %d", ErrInvalidConfiguration, bundle.SchemaVersion)
	}
	if bundle.Fragments == nil {
		return fmt.Errorf("%w: fragments must be an array", ErrInvalidConfiguration)
	}

	seenGroups := make(map[string]struct{}, len(bundle.Fragments))
	for index, fragment := range bundle.Fragments {
		groupID := strings.TrimSpace(fragment.GroupID)
		if groupID == "" {
			return fmt.Errorf("%w: fragment %d has an empty group_id", ErrInvalidConfiguration, index)
		}
		if _, exists := seenGroups[groupID]; exists {
			return fmt.Errorf("%w: duplicate group_id %q", ErrInvalidConfiguration, groupID)
		}
		seenGroups[groupID] = struct{}{}
		if fragment.GroupRevision == 0 {
			return fmt.Errorf("%w: fragment %d group_generation must be greater than zero", ErrInvalidConfiguration, index)
		}
		switch fragment.Engine {
		case agentv1.EngineXray, agentv1.EngineGOST:
		default:
			return fmt.Errorf("%w: fragment %d engine %q", ErrUnsupportedEngine, index, fragment.Engine)
		}
		if err := validateJSONObject(fragment.Config); err != nil {
			return fmt.Errorf("%w: fragment %d config: %v", ErrInvalidConfiguration, index, err)
		}
	}
	return nil
}

func validateJSONObject(raw json.RawMessage) error {
	if len(raw) == 0 {
		return errors.New("is empty")
	}
	var object map[string]json.RawMessage
	if err := decodeJSON(raw, &object); err != nil {
		return err
	}
	if object == nil {
		return errors.New("must be a JSON object")
	}
	return nil
}

func normalizeSHA256(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != sha256.Size*2 {
		return "", fmt.Errorf("%w: config_sha256 must contain 64 hexadecimal characters", ErrInvalidConfiguration)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", fmt.Errorf("%w: config_sha256 must contain 64 hexadecimal characters", ErrInvalidConfiguration)
	}
	return value, nil
}

func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("contains multiple JSON values")
		}
		return err
	}
	return nil
}

func decodeJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("contains multiple JSON values")
		}
		return err
	}
	return nil
}
