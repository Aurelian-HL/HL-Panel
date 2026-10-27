package postgressnapshot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

func TestPreparedWriteConsumesDetachedStateAndDiscardsRollback(t *testing.T) {
	ctx := context.Background()
	admin := bootstrapAdministrator(t)
	s, err := Open(ctx, isolatedDatabaseURL(t), admin)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var private *memoryrepo.Store
	if err := mutate(ctx, s, func(state *memoryrepo.Store) error {
		private = state
		return state.AppendAudit(ctx, audit.Event{ID: "committed"})
	}); err != nil {
		t.Fatal(err)
	}
	prepared := s.preparedWrite
	if prepared.state == nil || prepared.state == private {
		t.Fatal("write retained mutable caller state")
	}
	// A retained reference from a completed callback cannot contaminate the
	// independent restore consumed by the next transaction.
	if err := private.UpdateAdministratorPassword(ctx, admin.ID, admin.PasswordHash, admin.PasswordHash, true, audit.Event{}); err != nil {
		t.Fatal(err)
	}
	rollback := errors.New("isolated mutation rejection")
	if err := mutate(ctx, s, func(state *memoryrepo.Store) error {
		if state != prepared.state {
			t.Error("matching prepared state was decoded again")
		}
		current, err := state.AdministratorByID(ctx, admin.ID)
		if err != nil || current.MustChangePassword {
			t.Error("caller reference contaminated subsequent write", err)
		}
		if err := state.UpdateAdministratorPassword(ctx, admin.ID, admin.PasswordHash, admin.PasswordHash, true, audit.Event{ID: "rolled-back"}); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal("write did not reject mutation", err)
	}
	if s.preparedWrite.state != nil {
		t.Fatal("failed write retained its mutated prepared state")
	}
	if err := mutate(ctx, s, func(state *memoryrepo.Store) error {
		current, err := state.AdministratorByID(ctx, admin.ID)
		if err != nil || current.MustChangePassword || state == prepared.state {
			t.Error("rollback contaminated later mutation", err)
		}
		return state.AppendAudit(ctx, audit.Event{ID: "after-rollback"})
	}); err != nil {
		t.Fatal(err)
	}
	events, err := s.AuditEvents(ctx)
	if err != nil || len(events) != 2 {
		t.Fatal("write reuse leaked or lost audit events", len(events), err)
	}
}

func TestPreparedWriteRevalidatesOtherStoreAndSameRevisionRepair(t *testing.T) {
	for _, repair := range []bool{false, true} {
		name := "other-store"
		if repair {
			name = "same-revision-repair"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			admin := bootstrapAdministrator(t)
			dsn := isolatedDatabaseURL(t)
			s, err := Open(ctx, dsn, admin)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			other, err := Open(ctx, dsn, admin)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			if err := s.AppendAudit(ctx, audit.Event{ID: "seed"}); err != nil {
				t.Fatal(err)
			}
			stale := s.preparedWrite
			if repair {
				tx, err := other.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				state, err := readState(ctx, tx, true)
				if err != nil {
					t.Fatal(err)
				}
				if err := state.UpdateAdministratorPassword(ctx, admin.ID, admin.PasswordHash, admin.PasswordHash, true, audit.Event{}); err != nil {
					t.Fatal(err)
				}
				raw, err := encodeHotSnapshot(state)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := tx.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET payload=$1 WHERE singleton=true`, raw); err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			} else if err := other.UpdateAdministratorPassword(ctx, admin.ID, admin.PasswordHash, admin.PasswordHash, true, audit.Event{ID: "external"}); err != nil {
				t.Fatal(err)
			}
			if err := mutate(ctx, s, func(state *memoryrepo.Store) error {
				current, err := state.AdministratorByID(ctx, admin.ID)
				if err != nil || !current.MustChangePassword || state == stale.state {
					t.Error("stale prepared state hid external update", err)
				}
				return state.AppendAudit(ctx, audit.Event{ID: "after-external"})
			}); err != nil {
				t.Fatal(err)
			}
			// Even a same-revision unreadable repair must fail rather than use
			// a previously validated, but now obsolete, state.
			if _, err := other.db.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET payload=$1 WHERE singleton=true`, []byte(`broken`)); err != nil {
				t.Fatal(err)
			}
			calls := 0
			err = mutate(ctx, s, func(*memoryrepo.Store) error { calls++; return nil })
			if err == nil || calls != 0 || s.preparedWrite.state != nil {
				t.Fatal("prepared state bypassed corrupt database replacement", err)
			}
		})
	}
}

func TestPreparedWriteHealthRenewalFoldsOnlyCommittedOverlay(t *testing.T) {
	for _, commitFailure := range []bool{false, true} {
		name := "committed"
		if commitFailure {
			name = "deferred-commit-failure"
		}
		t.Run(name, func(t *testing.T) {
			s, _, proof := protocolHealthFixture(t)
			ctx := context.Background()
			// First activation re-enters the normal write path. A single
			// connection makes a leaked transaction or gate deadlock visible.
			s.db.SetMaxOpenConns(1)
			activation, cancel := context.WithTimeout(ctx, 3*time.Second)
			results, err := s.PublishProtocolHealthBatch(activation, []gatewaymembership.ProtocolObservation{proof})
			cancel()
			if err != nil || len(results) != 1 || results[0] != nil {
				t.Fatal("activation deadlocked or failed", results, err)
			}
			prepared := s.preparedWrite
			if prepared.state == nil {
				t.Fatal("activation did not prepare next writer")
			}
			if commitFailure {
				if _, err := s.db.ExecContext(ctx, `CREATE FUNCTION reject_prepared_health_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'isolated health failure'; RETURN NEW; END $$;
CREATE CONSTRAINT TRIGGER reject_prepared_health_commit AFTER UPDATE ON nyvp_control_snapshots DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_prepared_health_commit()`); err != nil {
					t.Fatal(err)
				}
			}
			next := proof
			next.VerifiedAt = next.VerifiedAt.Add(time.Second)
			next.LeaseExpiresAt = next.LeaseExpiresAt.Add(time.Second)
			err = s.PublishProtocolHealth(ctx, next)
			if commitFailure {
				if err == nil || s.preparedWrite != prepared {
					t.Fatal("failed renewal published prepared metadata", err)
				}
				if _, err := s.db.ExecContext(ctx, `DROP TRIGGER reject_prepared_health_commit ON nyvp_control_snapshots; DROP FUNCTION reject_prepared_health_commit()`); err != nil {
					t.Fatal(err)
				}
			} else {
				if err != nil || s.preparedWrite.state != prepared.state || s.preparedWrite.revision <= prepared.revision || s.preparedWrite.transactionID == prepared.transactionID {
					t.Fatal("renewal discarded matching restore or lost MVCC identity", err)
				}
				proof = next
			}
			if err := mutate(ctx, s, func(state *memoryrepo.Store) error {
				if state != prepared.state {
					t.Error("renewal forced redundant payload decode")
				}
				states, err := state.ProtocolProbeStates(ctx)
				if err != nil || len(states) != 1 || !states[0].State.ProtocolHealth[proof.NodeID].VerifiedAt.Equal(proof.VerifiedAt) {
					t.Error("prepared state lost committed proof or folded failed proof", err)
				}
				return state.AppendAudit(ctx, audit.Event{ID: "fold-prepared-proof"})
			}); err != nil {
				t.Fatal(err)
			}
			assertHealth(t, s, proof)
		})
	}
}

func TestPreparedWriteHealthRenewalDoesNotRelabelStalePayload(t *testing.T) {
	s, dsn, proof := protocolHealthFixture(t)
	ctx := context.Background()
	if err := s.PublishProtocolHealth(ctx, proof); err != nil {
		t.Fatal(err)
	}
	if s.preparedWrite.state == nil {
		t.Fatal("missing prepared fixture")
	}
	other, err := Open(ctx, dsn, bootstrapAdministrator(t))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	admin, err := other.AdministratorByID(ctx, "admin-one")
	if err != nil {
		t.Fatal(err)
	}
	if err := other.UpdateAdministratorPassword(ctx, admin.ID, admin.PasswordHash, admin.PasswordHash, true, audit.Event{ID: "external-password-change"}); err != nil {
		t.Fatal(err)
	}
	proof.VerifiedAt = proof.VerifiedAt.Add(time.Second)
	proof.LeaseExpiresAt = proof.LeaseExpiresAt.Add(time.Second)
	if err := s.PublishProtocolHealth(ctx, proof); err != nil {
		t.Fatal(err)
	}
	if s.preparedWrite.state != nil {
		t.Fatal("renewal relabeled stale payload as current")
	}
	if err := s.AppendAudit(ctx, audit.Event{ID: "after-external-renewal"}); err != nil {
		t.Fatal(err)
	}
	actual, err := s.AdministratorByID(ctx, admin.ID)
	if err != nil || !actual.MustChangePassword {
		t.Fatal("business write after renewal lost external update", err)
	}
	assertHealth(t, s, proof)
}
