package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// MixedProcessAdapter applies a complete node generation to two independently
// owned processes. It reports live only when both are running. Cross-process
// activation cannot be atomic; the reconciler restores the previous complete
// generation whenever a commit or verification fails.
type MixedProcessAdapter struct {
	xray       *XrayProcessAdapter
	gost       *GOSTProcessAdapter
	mu         sync.Mutex
	expectXray bool
	expectGOST bool
}

func NewMixedProcessAdapter(xray *XrayProcessAdapter, gost *GOSTProcessAdapter) (*MixedProcessAdapter, error) {
	if xray == nil || gost == nil || !xray.RequiresLiveProcess() || !gost.RequiresLiveProcess() {
		return nil, errors.New("mixed engine requires agent-owned Xray and GOST processes")
	}
	return &MixedProcessAdapter{xray: xray, gost: gost}, nil
}

func (a *MixedProcessAdapter) Prepare(ctx context.Context, desired agentv1.DesiredNodeConfig) (PreparedConfiguration, error) {
	prepared, err := parseDesired(desired)
	if err != nil {
		return PreparedConfiguration{}, err
	}
	prepared.components = make(map[agentv1.Engine]PreparedConfiguration)
	for _, engine := range []agentv1.Engine{agentv1.EngineXray, agentv1.EngineGOST} {
		fragments := make([]agentv1.ConfigurationFragment, 0)
		for _, fragment := range prepared.Bundle.Fragments {
			if fragment.Engine == engine {
				fragments = append(fragments, fragment)
			}
		}
		if len(fragments) == 0 {
			continue
		}
		config, err := json.Marshal(agentv1.ConfigurationBundle{SchemaVersion: BundleSchemaVersion, Fragments: fragments})
		if err != nil {
			return PreparedConfiguration{}, ErrInvalidConfiguration
		}
		hash := sha256.Sum256(config)
		part := agentv1.DesiredNodeConfig{Generation: desired.Generation, Engine: agentv1.EngineNodeBundle,
			ConfigSHA256: hex.EncodeToString(hash[:]), Config: config}
		var staged PreparedConfiguration
		if engine == agentv1.EngineXray {
			staged, err = a.xray.Prepare(ctx, part)
		} else {
			staged, err = a.gost.Prepare(ctx, part)
		}
		if err != nil {
			return PreparedConfiguration{}, fmt.Errorf("prepare %s component: %w", engine, err)
		}
		prepared.components[engine] = staged
	}
	return prepared, nil
}

func (a *MixedProcessAdapter) Validate(ctx context.Context, prepared PreparedConfiguration) error {
	if err := a.checkOwner(prepared); err != nil {
		return err
	}
	if part, ok := prepared.components[agentv1.EngineXray]; ok {
		if err := a.xray.Validate(ctx, part); err != nil {
			return fmt.Errorf("validate xray component: %w", err)
		}
	}
	if part, ok := prepared.components[agentv1.EngineGOST]; ok {
		if err := a.gost.Validate(ctx, part); err != nil {
			return fmt.Errorf("validate gost component: %w", err)
		}
	}
	return nil
}

func (a *MixedProcessAdapter) Commit(ctx context.Context, prepared PreparedConfiguration) error {
	if err := a.Validate(ctx, prepared); err != nil {
		return err
	}
	a.mu.Lock()
	a.expectXray, a.expectGOST = false, false
	a.mu.Unlock()
	if part, ok := prepared.components[agentv1.EngineXray]; ok {
		if err := a.xray.Commit(ctx, part); err != nil {
			return fmt.Errorf("commit xray component: %w", err)
		}
	} else if err := a.xray.Rollback(ctx, nil); err != nil {
		return fmt.Errorf("stop obsolete xray component: %w", err)
	}
	if part, ok := prepared.components[agentv1.EngineGOST]; ok {
		if err := a.gost.Commit(ctx, part); err != nil {
			return fmt.Errorf("commit gost component: %w", err)
		}
	} else if err := a.gost.Rollback(ctx, nil); err != nil {
		return fmt.Errorf("stop obsolete gost component: %w", err)
	}
	a.mu.Lock()
	_, a.expectXray = prepared.components[agentv1.EngineXray]
	_, a.expectGOST = prepared.components[agentv1.EngineGOST]
	a.mu.Unlock()
	return nil
}

func (a *MixedProcessAdapter) Verify(ctx context.Context, prepared PreparedConfiguration) error {
	if err := a.checkOwner(prepared); err != nil {
		return err
	}
	for _, engine := range []agentv1.Engine{agentv1.EngineXray, agentv1.EngineGOST} {
		part, ok := prepared.components[engine]
		if !ok {
			if engine == agentv1.EngineXray && a.xray.ActiveEngineMode() != "" ||
				engine == agentv1.EngineGOST && a.gost.ActiveEngineMode() != "" {
				return fmt.Errorf("verify %s component: obsolete process is still running", engine)
			}
			continue
		}
		var err error
		if engine == agentv1.EngineXray {
			err = a.xray.Verify(ctx, part)
		} else {
			err = a.gost.Verify(ctx, part)
		}
		if err != nil {
			return fmt.Errorf("verify %s component: %w", engine, err)
		}
	}
	return nil
}

func (a *MixedProcessAdapter) Rollback(ctx context.Context, previous *PreparedConfiguration) error {
	if previous != nil {
		if err := a.checkOwner(*previous); err != nil {
			return err
		}
	}
	a.mu.Lock()
	a.expectXray, a.expectGOST = false, false
	a.mu.Unlock()
	var firstErr error
	for _, engine := range []agentv1.Engine{agentv1.EngineXray, agentv1.EngineGOST} {
		var component *PreparedConfiguration
		if previous != nil {
			if part, ok := previous.components[engine]; ok {
				component = &part
			}
		}
		var err error
		if engine == agentv1.EngineXray {
			err = a.xray.Rollback(ctx, component)
		} else {
			err = a.gost.Rollback(ctx, component)
		}
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("restore %s component: %w", engine, err)
		}
	}
	if firstErr == nil && previous != nil {
		a.mu.Lock()
		_, a.expectXray = previous.components[agentv1.EngineXray]
		_, a.expectGOST = previous.components[agentv1.EngineGOST]
		a.mu.Unlock()
	}
	return firstErr
}

func (a *MixedProcessAdapter) ActiveEngineMode() string {
	a.mu.Lock()
	expectXray, expectGOST := a.expectXray, a.expectGOST
	a.mu.Unlock()
	xrayRunning := a.xray.ActiveEngineMode() == "xray"
	gostRunning := a.gost.ActiveEngineMode() == "gost"
	if expectXray != xrayRunning || expectGOST != gostRunning {
		return ""
	}
	if expectXray && expectGOST {
		return "mixed"
	}
	if expectXray {
		return "xray"
	}
	if expectGOST {
		return "gost"
	}
	return ""
}

func (a *MixedProcessAdapter) RequiresLiveProcess() bool { return true }

func (a *MixedProcessAdapter) checkOwner(prepared PreparedConfiguration) error {
	if prepared.components == nil {
		return ErrPreparedForOtherEngine
	}
	for engine, part := range prepared.components {
		var err error
		switch engine {
		case agentv1.EngineXray:
			err = a.xray.checkOwner(part)
		case agentv1.EngineGOST:
			err = a.gost.checkOwner(part)
		default:
			return ErrUnsupportedEngine
		}
		if err != nil {
			return err
		}
	}
	return nil
}
