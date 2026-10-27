package postgressnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/panelquota"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestSnapshotFailureRedactsCausesAndPreservesStage(t *testing.T) {
	const secret = "isolated-secret-password-payload"
	for _, test := range []struct {
		name, stage, category, sqlState, outcome string
		cause                                    error
	}{
		{"begin", "begin", "internal", "none", "not_committed", errors.New(secret)},
		{"lock cancellation", "snapshot_lock", "statement_canceled", "57014", "not_committed", &pgconn.PgError{Code: "57014", Message: secret, Detail: secret}},
		{"commit rejection", "commit", "database", "23514", "unconfirmed", &pgconn.PgError{Code: "23514", Message: secret, Detail: secret}},
		{"invalid sqlstate", "snapshot_update", "internal", "none", "not_committed", &pgconn.PgError{Code: secret, Message: secret}},
		{"decoder validation", "snapshot_decode", "internal", "none", "not_committed", fmt.Errorf("%w: %s", faults.ErrValidation, secret)},
		{"decoder authentication", "snapshot_validate", "internal", "none", "not_committed", fmt.Errorf("%w: %s", faults.ErrUnauthorized, secret)},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := snapshotFailure(context.Background(), test.stage, test.cause)
			var failure *snapshotTransactionFailure
			if !errors.As(err, &failure) || failure.stage != test.stage || failure.category != test.category || failure.sqlState != test.sqlState || failure.outcome != test.outcome {
				t.Fatal("incorrect failure classification", err)
			}
			if errors.Is(err, faults.ErrValidation) || errors.Is(err, faults.ErrUnauthorized) {
				t.Fatal("persistence corruption became a client error")
			}
			var logs bytes.Buffer
			slog.New(slog.NewJSONHandler(&logs, nil)).Warn("isolated failure", "error", err)
			raw, marshalErr := json.Marshal(err)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			for _, output := range []string{err.Error(), fmt.Sprintf("%+v", err), failure.SafePersistenceDiagnostic(), logs.String(), string(raw)} {
				if strings.Contains(output, secret) {
					t.Fatal("diagnostics exposed a cause or payload")
				}
			}
			if wrapped := snapshotFailure(context.Background(), "operation", err); wrapped != err {
				t.Fatal("outer operation replaced precise persistence stage")
			}
		})
	}
}

func TestSnapshotFailurePreservesCancellationAndBusinessRejections(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		err := snapshotFailure(context.Background(), "write_queue", cause)
		if !errors.Is(err, cause) {
			t.Fatal("cancellation identity lost")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := snapshotFailure(ctx, "snapshot_lock", &pgconn.PgError{Code: "57014", Message: "isolated cancel"})
	var failure *snapshotTransactionFailure
	if !errors.As(err, &failure) || failure.category != "canceled" || failure.sqlState != "57014" || !errors.Is(err, context.Canceled) {
		t.Fatal("driver cancellation lost context or SQLSTATE", err)
	}
	for _, sentinel := range []error{faults.ErrValidation, faults.ErrUnauthorized, faults.ErrExpired, faults.ErrAlreadyUsed,
		faults.ErrNotFound, faults.ErrConflict, faults.ErrIdempotencyConflict, faults.ErrUnavailable, faults.ErrPasswordChangeRequired, panelquota.ErrExceeded} {
		rejection := fmt.Errorf("%w: isolated business detail", sentinel)
		if actual := snapshotOperationFailure(context.Background(), rejection); actual != rejection {
			t.Fatal("business rejection message or classification changed", sentinel)
		}
	}
	rejection := errors.New("isolated-private-operation-failure")
	err = snapshotOperationFailure(context.Background(), rejection)
	if !errors.Is(err, rejection) || strings.Contains(err.Error(), rejection.Error()) {
		t.Fatal("unexpected rejection lost identity or exposed details")
	}
}
