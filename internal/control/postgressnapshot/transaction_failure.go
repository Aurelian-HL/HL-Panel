package postgressnapshot

import (
	"context"
	"errors"
	"fmt"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/panelquota"
)

// Retain operation rejection identity and canonical context errors. Discard
// driver/decode causes after classification, so corrupt persisted state cannot
// masquerade as a client validation or authentication failure. Only locally
// authored stages and bounded categories reach diagnostic logs.
type snapshotTransactionFailure struct {
	stage    string
	category string
	sqlState string
	outcome  string
	cause    error
}

func (e *snapshotTransactionFailure) Error() string {
	return "PostgreSQL persistence failed: " + e.SafePersistenceDiagnostic()
}

func (e *snapshotTransactionFailure) Unwrap() error { return e.cause }

func (e *snapshotTransactionFailure) SafePersistenceDiagnostic() string {
	return fmt.Sprintf("stage=%s category=%s sql_state=%s outcome=%s", e.stage, e.category, e.sqlState, e.outcome)
}

func snapshotFailure(ctx context.Context, stage string, cause error) error {
	if cause == nil {
		return nil
	}
	var existing *snapshotTransactionFailure
	if errors.As(cause, &existing) {
		return cause
	}
	failure := &snapshotTransactionFailure{stage: stage, category: "internal", sqlState: "none", outcome: "not_committed"}
	if stage == "operation" {
		failure.cause = cause
	}
	// Failure to receive a COMMIT acknowledgement cannot establish whether the
	// transaction committed. Do not automatically replay the business callback.
	if stage == "commit" {
		failure.outcome = "unconfirmed"
	}
	var driverError interface{ SQLState() string }
	if errors.As(cause, &driverError) {
		code := driverError.SQLState()
		if validSQLState(code) {
			failure.sqlState, failure.category = code, "database"
		}
	}
	switch {
	case errors.Is(cause, context.DeadlineExceeded), errors.Is(ctx.Err(), context.DeadlineExceeded):
		failure.category = "deadline"
		if failure.cause == nil {
			failure.cause = context.DeadlineExceeded
		}
	case errors.Is(cause, context.Canceled), errors.Is(ctx.Err(), context.Canceled):
		failure.category = "canceled"
		if failure.cause == nil {
			failure.cause = context.Canceled
		}
	case failure.sqlState == "57014":
		failure.category = "statement_canceled"
	}
	return failure
}

func validSQLState(code string) bool {
	if len(code) != 5 {
		return false
	}
	for _, char := range code {
		if (char < '0' || char > '9') && (char < 'A' || char > 'Z') {
			return false
		}
	}
	return true
}

func snapshotOperationFailure(ctx context.Context, cause error) error {
	// Preserve public validation/conflict messages and their existing status
	// codes. An unexpected operation failure gets a redacted diagnostic, while
	// its original identity remains discoverable through errors.Is/As.
	for _, expected := range []error{faults.ErrValidation, faults.ErrUnauthorized, faults.ErrExpired,
		faults.ErrAlreadyUsed, faults.ErrNotFound, faults.ErrConflict, faults.ErrIdempotencyConflict,
		faults.ErrUnavailable, faults.ErrPasswordChangeRequired, panelquota.ErrExceeded} {
		if errors.Is(cause, expected) {
			return cause
		}
	}
	return snapshotFailure(ctx, "operation", cause)
}
