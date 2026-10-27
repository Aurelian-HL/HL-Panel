package postgressnapshot

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/rulegroups"
)

// Reproduce the cancellation mechanism against a disposable schema, not the
// customer's historical request. A PostgreSQL lock_timeout (55P03) is different
// from a caller cancellation or PostgreSQL user-request cancellation (57014).
func TestPausedRuleLockCancellationLeavesStateAndAllowsRetry(t *testing.T) {
	for _, mode := range []string{"request_canceled", "request_deadline", "database_cancel"} {
		t.Run(mode, func(t *testing.T) {
			s, _, request, _, _ := ruleDeletionPGFixture(t)
			ctx, stop := context.WithTimeout(context.Background(), 12*time.Second)
			defer stop()
			request.Operation = rulegroups.BatchPause
			before, beforeRevision := healthSnapshot(t, s)
			beforeAudit, err := s.AuditEvents(ctx)
			if err != nil {
				t.Fatal(err)
			}
			blocking, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer blocking.Rollback()
			var blockerPID int
			if err := blocking.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&blockerPID); err != nil {
				t.Fatal(err)
			}
			if _, err := blocking.ExecContext(ctx, "SELECT singleton FROM nyvp_control_snapshots WHERE singleton=true FOR UPDATE"); err != nil {
				t.Fatal(err)
			}
			waiting, cancel := context.WithCancel(ctx)
			if mode == "request_deadline" {
				cancel()
				waiting, cancel = context.WithTimeout(ctx, 2*time.Second)
			}
			defer cancel()
			service := rulegroups.NewService(s, time.Now)
			result := make(chan error, 1)
			go func() {
				_, _, err := service.BatchForAdministrator(waiting, "admin-one", request, "pause-cancel-retry")
				result <- err
			}()
			// Observe the actual database lock wait before triggering cancellation.
			var waiterPID int
			observed := time.Now().Add(time.Second)
			for waiterPID == 0 && time.Now().Before(observed) {
				if err := s.db.QueryRowContext(ctx, "SELECT coalesce(max(pid),0) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND $1=ANY(pg_blocking_pids(pid))", blockerPID).Scan(&waiterPID); err != nil {
					t.Fatal(err)
				}
				if waiterPID == 0 {
					time.Sleep(5 * time.Millisecond)
				}
			}
			if waiterPID == 0 {
				t.Fatal("pause request never entered the database row lock wait")
			}
			switch mode {
			case "request_canceled":
				cancel()
			case "database_cancel":
				var canceled bool
				if err := s.db.QueryRowContext(ctx, "SELECT pg_cancel_backend($1)", waiterPID).Scan(&canceled); err != nil || !canceled {
					t.Fatal("isolated waiting backend was not canceled", err)
				}
			}
			select {
			case err = <-result:
			case <-ctx.Done():
				t.Fatal("canceled pause request did not finish")
			}
			var failure *snapshotTransactionFailure
			if !errors.As(err, &failure) || failure.stage != "snapshot_lock" || failure.outcome != "not_committed" {
				t.Fatal("canceled pause lost failure stage or commit outcome", err)
			}
			switch mode {
			case "request_canceled":
				if !errors.Is(err, context.Canceled) || failure.category != "canceled" {
					t.Fatal("request cancellation lost its identity", err)
				}
			case "request_deadline":
				if !errors.Is(err, context.DeadlineExceeded) || failure.category != "deadline" {
					t.Fatal("request deadline lost its identity", err)
				}
			case "database_cancel":
				if failure.sqlState != "57014" || failure.category != "statement_canceled" {
					t.Fatal("PostgreSQL user-request cancellation was confused with lock_timeout", err)
				}
			}
			after, afterRevision := healthSnapshot(t, s)
			if beforeRevision != afterRevision || !bytes.Equal(before, after) || s.preparedWrite.state != nil {
				t.Fatal("canceled pause changed durable state or retained failed prepared state")
			}
			afterAudit, err := s.AuditEvents(ctx)
			if err != nil || len(afterAudit) != len(beforeAudit) {
				t.Fatal("canceled pause committed an audit entry", err)
			}
			if err := blocking.Rollback(); err != nil {
				t.Fatal(err)
			}
			paused, replay, err := service.BatchForAdministrator(ctx, "admin-one", request, "pause-cancel-retry")
			if err != nil || replay || len(paused.Items) != 1 || !paused.Items[0].Paused {
				t.Fatal("canceled pause prevented a valid retry with the same idempotency key", err)
			}
			if _, replay, err := service.BatchForAdministrator(ctx, "admin-one", request, "pause-cancel-retry"); err != nil || !replay {
				t.Fatal("committed pause retry did not replay idempotently", err)
			}
			t.Logf("%s: %s; no mutation, audit or idempotency commit; subsequent pause and replay succeed", mode, failure.SafePersistenceDiagnostic())
		})
	}
}

func TestSnapshotLockFailureIdentifiesSQLStateAndAllowsRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := Open(ctx, isolatedDatabaseURL(t), bootstrapAdministrator(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	blocking, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocking.Rollback()
	if _, err := blocking.ExecContext(ctx, `SELECT singleton FROM nyvp_control_snapshots WHERE singleton=true FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	waiting, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer waiting.Rollback()
	if _, err := waiting.ExecContext(ctx, `SET LOCAL lock_timeout='100ms'`); err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareSnapshotWrite(ctx, waiting)
	if err != nil {
		t.Fatal(err)
	}
	_, err = lockSnapshotWrite(ctx, waiting, prepared)
	var failure *snapshotTransactionFailure
	if !errors.As(err, &failure) || failure.stage != "snapshot_lock" || failure.sqlState != "55P03" || failure.outcome != "not_committed" {
		t.Fatal("lock timeout lost its failure stage or SQLSTATE", err)
	}
	if err := waiting.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := blocking.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendAudit(ctx, audit.Event{ID: "after-lock-timeout"}); err != nil {
		t.Fatal("isolated lock failure blocked later writes", err)
	}
}

func TestSnapshotDecodeFailureDoesNotBecomeClientRejection(t *testing.T) {
	ctx := context.Background()
	admin := bootstrapAdministrator(t)
	s, err := Open(ctx, isolatedDatabaseURL(t), admin)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.db.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET payload=$1,revision=revision+1 WHERE singleton=true`, []byte(`{"private-invalid-payload":true}`)); err != nil {
		t.Fatal(err)
	}
	_, err = s.AdministratorByID(ctx, admin.ID)
	var failure *snapshotTransactionFailure
	if !errors.As(err, &failure) || failure.stage != "snapshot_decode" || errors.Is(err, faults.ErrValidation) || errors.Is(err, faults.ErrUnauthorized) {
		t.Fatal("corrupt persisted state changed the HTTP rejection classification", err)
	}
}
