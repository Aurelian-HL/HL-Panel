// Package reconciler owns the local desired-to-applied generation state
// machine. It does not make business or routing decisions.
package reconciler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hongle/hl-panel/internal/agent/engine"
	"github.com/hongle/hl-panel/internal/agent/state"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type ResultReporter interface {
	ReportApplyResult(context.Context, string, agentv1.ApplyResultRequest) error
}

type Reconciler struct {
	store      *state.AtomicStateStore
	adapter    engine.EngineAdapter
	reporter   ResultReporter
	nodeSecret string
	now        func() time.Time
	mu         sync.Mutex
}

func New(store *state.AtomicStateStore, adapter engine.EngineAdapter, reporter ResultReporter, nodeSecret string) (*Reconciler, error) {
	if store == nil {
		return nil, errors.New("state store is required")
	}
	if adapter == nil {
		return nil, errors.New("engine adapter is required")
	}
	return &Reconciler{
		store:      store,
		adapter:    adapter,
		reporter:   reporter,
		nodeSecret: nodeSecret,
		now:        time.Now,
	}, nil
}

// Apply drives prepare -> validate -> commit -> verify. The applied marker is
// advanced only after both commit and local verification succeed.
func (r *Reconciler) Apply(ctx context.Context, desired agentv1.DesiredNodeConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := r.store.Load()
	if err != nil {
		return fmt.Errorf("load agent state: %w", err)
	}
	if current.Applied != nil && current.Applied.Generation == desired.Generation && current.Applied.Engine == desired.Engine && current.Applied.ConfigSHA256 == strings.ToLower(strings.TrimSpace(desired.ConfigSHA256)) {
		if process, ok := r.adapter.(interface {
			RequiresLiveProcess() bool
			ActiveEngineMode() string
		}); ok && process.RequiresLiveProcess() && process.ActiveEngineMode() == "" {
			return r.restoreAppliedLocked(ctx, desired)
		}
		_ = r.flushReceiptsLocked(ctx)
		return nil
	}
	attemptID, err := newAttemptID()
	if err != nil {
		return fmt.Errorf("create apply attempt id: %w", err)
	}

	previousDesired, hasPrevious, err := r.store.LastKnownGood()
	if err != nil {
		return fmt.Errorf("load last-known-good configuration: %w", err)
	}
	var previousPrepared *engine.PreparedConfiguration
	if hasPrevious {
		prepared, prepareErr := r.adapter.Prepare(ctx, previousDesired)
		if prepareErr != nil {
			return fmt.Errorf("prepare last-known-good rollback configuration: %w", prepareErr)
		}
		previousPrepared = &prepared
	}

	if _, err := r.store.Begin(desired, r.now()); err != nil {
		if errors.Is(err, state.ErrPendingApplyExists) {
			return fmt.Errorf("another generation is pending: %w", err)
		}
		return fmt.Errorf("record pending desired configuration: %w", err)
	}

	prepared, err := r.adapter.Prepare(ctx, desired)
	if err != nil {
		return r.fail(ctx, desired, attemptID, previousPrepared, agentv1.ApplyPhasePrepare, err)
	}
	r.report(ctx, desired, attemptID, agentv1.ApplyPhasePrepare, agentv1.ApplyStatusSucceeded, "configuration staged")

	if err := r.adapter.Validate(ctx, prepared); err != nil {
		return r.fail(ctx, desired, attemptID, previousPrepared, agentv1.ApplyPhaseValidate, err)
	}
	r.report(ctx, desired, attemptID, agentv1.ApplyPhaseValidate, agentv1.ApplyStatusSucceeded, "configuration validated")

	if err := r.adapter.Commit(ctx, prepared); err != nil {
		return r.fail(ctx, desired, attemptID, previousPrepared, agentv1.ApplyPhaseCommit, err)
	}
	if err := r.adapter.Verify(ctx, prepared); err != nil {
		return r.fail(ctx, desired, attemptID, previousPrepared, agentv1.ApplyPhaseVerify, err)
	}
	receipts := r.verifiedReceipts(desired, attemptID)
	if _, err := r.store.CompleteWithReceipts(desired, receipts, r.now()); err != nil {
		return r.fail(ctx, desired, attemptID, previousPrepared, agentv1.ApplyPhaseVerify, err)
	}
	_ = r.flushReceiptsLocked(ctx)
	return nil
}

// RecoverPending resumes a journaled apply after a process restart. Replaying
// the same generation is idempotent and keeps the previous applied marker until
// verify succeeds.
func (r *Reconciler) RecoverPending(ctx context.Context) error {
	pending, exists, err := r.store.PendingDesired()
	if err != nil {
		return fmt.Errorf("load pending configuration: %w", err)
	}
	if !exists {
		return nil
	}
	return r.Apply(ctx, pending)
}

// RestoreApplied reactivates the last-known-good configuration after an Agent
// restart. A persisted applied marker alone cannot attest a running process.
func (r *Reconciler) RestoreApplied(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	current, err := r.store.Load()
	if err != nil {
		return err
	}
	if current.Pending != nil || current.Applied == nil {
		return nil
	}
	desired, exists, err := r.store.LastKnownGood()
	if err != nil || !exists {
		return err
	}
	return r.restoreAppliedLocked(ctx, desired)
}

func (r *Reconciler) restoreAppliedLocked(ctx context.Context, desired agentv1.DesiredNodeConfig) error {
	if err := r.store.ClearApplyReceipts(); err != nil {
		return fmt.Errorf("clear previous process receipts: %w", err)
	}
	attemptID, err := newAttemptID()
	if err != nil {
		return fmt.Errorf("create restore attempt id: %w", err)
	}
	prepared, err := r.adapter.Prepare(ctx, desired)
	if err != nil {
		return r.restoreFailed(ctx, desired, attemptID, agentv1.ApplyPhasePrepare, err)
	}
	r.report(ctx, desired, attemptID, agentv1.ApplyPhasePrepare, agentv1.ApplyStatusSucceeded, "previous configuration staged")
	if err := r.adapter.Validate(ctx, prepared); err != nil {
		return r.restoreFailed(ctx, desired, attemptID, agentv1.ApplyPhaseValidate, err)
	}
	r.report(ctx, desired, attemptID, agentv1.ApplyPhaseValidate, agentv1.ApplyStatusSucceeded, "previous configuration validated")
	if err := r.adapter.Commit(ctx, prepared); err != nil {
		return r.restoreFailed(ctx, desired, attemptID, agentv1.ApplyPhaseCommit, err)
	}
	if err := r.adapter.Verify(ctx, prepared); err != nil {
		return r.restoreFailed(ctx, desired, attemptID, agentv1.ApplyPhaseVerify, err)
	}
	if _, err := r.store.RecordResultWithReceipts(agentv1.ApplyPhaseVerify, agentv1.ApplyStatusSucceeded,
		"previous configuration reactivated and verified", r.verifiedReceipts(desired, attemptID), r.now()); err != nil {
		return err
	}
	_ = r.flushReceiptsLocked(ctx)
	return nil
}

func (r *Reconciler) verifiedReceipts(desired agentv1.DesiredNodeConfig, attemptID string) []agentv1.ApplyResultRequest {
	mode := ""
	if process, ok := r.adapter.(interface{ ActiveEngineMode() string }); ok {
		mode = process.ActiveEngineMode()
	}
	base := agentv1.ApplyResultRequest{
		Generation: desired.Generation, ConfigSHA256: strings.ToLower(strings.TrimSpace(desired.ConfigSHA256)),
		AttemptID: attemptID, Status: agentv1.ApplyStatusSucceeded, EngineMode: mode,
	}
	commit := base
	commit.Phase = agentv1.ApplyPhaseCommit
	commit.Message = "configuration activated"
	verify := base
	verify.Phase = agentv1.ApplyPhaseVerify
	verify.Message = "configuration committed and verified"
	return []agentv1.ApplyResultRequest{commit, verify}
}

func (r *Reconciler) flushReceiptsLocked(ctx context.Context) error {
	if r.reporter == nil || r.nodeSecret == "" {
		return nil
	}
	receipts, err := r.store.PendingApplyReceipts()
	if err != nil {
		return err
	}
	for _, receipt := range receipts {
		if err := r.reporter.ReportApplyResult(ctx, r.nodeSecret, receipt); err != nil {
			return err
		}
		if err := r.store.AcknowledgeApplyReceipt(receipt); err != nil {
			return err
		}
	}
	return nil
}

// FlushReceipts retries durable terminal receipts even when the control plane
// no longer offers a desired generation to this node.
func (r *Reconciler) FlushReceipts(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.flushReceiptsLocked(ctx)
}

func (r *Reconciler) restoreFailed(ctx context.Context, desired agentv1.DesiredNodeConfig, attemptID string, phase agentv1.ApplyPhase, cause error) error {
	message := sanitizeMessage(cause.Error())
	r.report(ctx, desired, attemptID, phase, agentv1.ApplyStatusFailed, message)
	if err := r.adapter.Rollback(context.WithoutCancel(ctx), nil); err != nil {
		message = sanitizeMessage(message + "; rollback failed: " + err.Error())
	}
	_, _ = r.store.RecordResult(phase, agentv1.ApplyStatusFailed, message, r.now())
	return cause
}

func (r *Reconciler) fail(ctx context.Context, desired agentv1.DesiredNodeConfig, attemptID string, previous *engine.PreparedConfiguration, phase agentv1.ApplyPhase, applyErr error) error {
	message := sanitizeMessage(applyErr.Error())
	r.report(ctx, desired, attemptID, phase, agentv1.ApplyStatusFailed, message)
	// A canceled control-plane request must not cancel local safety recovery.
	rollbackErr := r.adapter.Rollback(context.WithoutCancel(ctx), previous)
	if rollbackErr != nil {
		message = sanitizeMessage(fmt.Sprintf("%s; rollback failed: %s", message, rollbackErr))
		_, _ = r.store.Abort(agentv1.ApplyPhaseRollback, agentv1.ApplyStatusFailed, message, r.now())
		r.report(ctx, desired, attemptID, agentv1.ApplyPhaseRollback, agentv1.ApplyStatusFailed, message)
		return fmt.Errorf("%w; rollback failed: %v", applyErr, rollbackErr)
	}
	_, _ = r.store.Abort(agentv1.ApplyPhaseRollback, agentv1.ApplyStatusRolledBack, message, r.now())
	r.report(ctx, desired, attemptID, agentv1.ApplyPhaseRollback, agentv1.ApplyStatusRolledBack, "previous configuration restored")
	return applyErr
}

func (r *Reconciler) report(ctx context.Context, desired agentv1.DesiredNodeConfig, attemptID string, phase agentv1.ApplyPhase, status agentv1.ApplyStatus, message string) {
	if r.reporter == nil || r.nodeSecret == "" {
		return
	}
	request := agentv1.ApplyResultRequest{
		Generation:   desired.Generation,
		Phase:        phase,
		Status:       status,
		ConfigSHA256: strings.ToLower(strings.TrimSpace(desired.ConfigSHA256)),
		AttemptID:    attemptID,
		Message:      sanitizeMessage(message),
	}
	if status == agentv1.ApplyStatusSucceeded && (phase == agentv1.ApplyPhaseCommit || phase == agentv1.ApplyPhaseVerify) {
		if process, ok := r.adapter.(interface{ ActiveEngineMode() string }); ok {
			request.EngineMode = process.ActiveEngineMode()
		}
	}
	// The local state machine is authoritative while disconnected. A failed
	// telemetry call must not make a verified local configuration unsafe.
	_ = r.reporter.ReportApplyResult(ctx, r.nodeSecret, request)
}

func newAttemptID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(random[:]), nil
}

func sanitizeMessage(message string) string {
	message = strings.Map(func(character rune) rune {
		if character == '\n' || character == '\r' || character == '\t' {
			return ' '
		}
		if character < 0x20 || character == 0x7f {
			return -1
		}
		return character
	}, message)
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > state.MaxApplyMessageBytes {
		return message[:state.MaxApplyMessageBytes]
	}
	return message
}
