package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
)

// Normal configuration delivery never replaces a live process. These errors
// retain the data plane while refusing to attest an unapplied generation.
var (
	ErrLiveUpdateRequiresMaintenance = errors.New("live update cannot safely apply this change; existing process preserved; maintenance required")
	ErrLiveStateUncertain            = errors.New("live update outcome is uncertain; existing process preserved and further changes blocked; maintenance required")
)

// PreservesLiveProcess lets recovery avoid turning a deliberate refusal or an
// uncertain RPC into Rollback(nil), which would stop healthy mixed components.
func PreservesLiveProcess(err error) bool {
	return errors.Is(err, ErrLiveUpdateRequiresMaintenance) || errors.Is(err, ErrLiveStateUncertain)
}

// HasLiveProcess includes partial mixed-engine availability, for which
// ActiveEngineMode deliberately cannot attest the complete desired bundle.
func (a *XrayProcessAdapter) HasLiveProcess() bool { return a.ActiveEngineMode() != "" }
func (a *GOSTProcessAdapter) HasLiveProcess() bool { return a.ActiveEngineMode() != "" }
func (a *MixedProcessAdapter) HasLiveProcess() bool {
	return a.xray.HasLiveProcess() || a.gost.HasLiveProcess()
}

func (a *XrayProcessAdapter) LiveUpdateError() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.liveStateUncertain && a.process != nil && a.process.Running() {
		return ErrLiveStateUncertain
	}
	return nil
}

func (a *GOSTProcessAdapter) LiveUpdateError() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.liveStateUncertain && a.process != nil && a.process.Running() {
		return ErrLiveStateUncertain
	}
	return nil
}

func (a *MixedProcessAdapter) LiveUpdateError() error {
	return errors.Join(a.xray.LiveUpdateError(), a.gost.LiveUpdateError())
}

// Preflight all live components before the first mixed-engine mutation. It
// cannot predict an RPC failure, but prevents a known unsupported GOST change
// from first disturbing Xray handlers that would then need to be rolled back.
func (a *XrayProcessAdapter) checkLiveChange(ctx context.Context, prepared PreparedConfiguration) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !a.autoStart || a.process == nil || !a.process.Running() {
		return nil
	}
	if a.liveStateUncertain {
		return ErrLiveStateUncertain
	}
	previous, exists, err := readOptionalRegularFile(a.activePath)
	if err != nil {
		return err
	}
	candidate, err := readRegularFile(prepared.adapterPath, MaxConfigurationBytes)
	if err != nil {
		return err
	}
	if exists && a.processConfigSHA256 == sha256.Sum256(previous) {
		if bytes.Equal(previous, candidate) {
			return nil
		}
		if _, ok := a.runner.(xrayAPIRunner); ok {
			if _, eligible := planXrayAdditions(previous, candidate); eligible {
				return nil
			}
			if _, eligible := planXrayLiveChanges(previous, candidate); eligible {
				return nil
			}
		}
	}
	return ErrLiveUpdateRequiresMaintenance
}

func (a *GOSTProcessAdapter) checkLiveChange(ctx context.Context, prepared PreparedConfiguration) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !a.autoStart || a.process == nil || !a.process.Running() {
		return nil
	}
	if a.liveStateUncertain {
		return ErrLiveStateUncertain
	}
	previous, exists, err := readOptionalRegularFile(a.activePath)
	if err != nil {
		return err
	}
	candidate, err := readRegularFile(prepared.adapterPath, MaxConfigurationBytes)
	if err != nil {
		return err
	}
	if exists && a.processConfigSHA256 == sha256.Sum256(previous) {
		if bytes.Equal(previous, candidate) {
			return nil
		}
		if _, ok := a.runner.(gostAPIRunner); ok && a.api != nil {
			if _, eligible := planGOSTLiveChanges(previous, candidate); eligible {
				return nil
			}
		}
	}
	return ErrLiveUpdateRequiresMaintenance
}
