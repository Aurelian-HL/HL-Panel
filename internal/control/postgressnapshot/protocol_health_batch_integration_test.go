package postgressnapshot

import (
	"bytes"
	"context"
	"database/sql"
	"reflect"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
)

func TestProtocolHealthBatchRenewalOrderedProofsAndCommitFailure(t *testing.T) {
	for _, commitFailure := range []bool{false, true} {
		name := "committed"
		if commitFailure {
			name = "deferred-commit-failure"
		}
		t.Run(name, func(t *testing.T) {
			s, dsn, proof := protocolHealthFixture(t)
			ctx := context.Background()
			if results, err := s.PublishProtocolHealthBatch(ctx, []gatewaymembership.ProtocolObservation{proof}); err != nil || results[0] != nil {
				t.Fatalf("activation: %v %v", results, err)
			}
			other, err := Open(ctx, dsn, bootstrapAdministrator(t))
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			assertHealth(t, other, proof)
			before, revision := healthSnapshot(t, s)
			projectionRevision := healthProjectionRevision(t, s)
			audits, err := s.AuditEvents(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if commitFailure {
				_, err := s.db.Exec(`CREATE FUNCTION reject_health_commit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'isolated health commit rejection'; RETURN NEW; END $$;
CREATE CONSTRAINT TRIGGER reject_health_commit AFTER UPDATE ON nyvp_control_snapshots
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_health_commit()`)
				if err != nil {
					t.Fatal(err)
				}
			}
			newer := proof
			newer.VerifiedAt = newer.VerifiedAt.Add(time.Second)
			newer.LeaseExpiresAt = newer.LeaseExpiresAt.Add(time.Second)
			latest := newer
			latest.VerifiedAt = latest.VerifiedAt.Add(time.Second)
			latest.LeaseExpiresAt = latest.LeaseExpiresAt.Add(time.Second)
			invalid := latest
			invalid.ConfigSHA256 = "invalid"
			results, err := s.PublishProtocolHealthBatch(ctx, []gatewaymembership.ProtocolObservation{newer, proof, latest, invalid})
			if commitFailure {
				if err == nil {
					t.Fatal("deferred failure accepted")
				}
			} else if err != nil || len(results) != 4 || results[0] != nil || results[1] == nil || results[2] != nil || results[3] == nil {
				t.Fatalf("ordered proof results: %v %v", results, err)
			}
			after, next := healthSnapshot(t, s)
			wantRevision := revision + 1
			wantProof := latest
			if commitFailure {
				wantRevision, wantProof = revision, proof
			}
			if !bytes.Equal(before, after) || next != wantRevision {
				t.Fatal("renewal snapshot payload/revision mismatch")
			}
			assertHealth(t, s, wantProof)
			assertHealth(t, other, wantProof)
			cold, err := Open(ctx, dsn, bootstrapAdministrator(t))
			if err != nil {
				t.Fatal(err)
			}
			defer cold.Close()
			assertHealth(t, cold, wantProof)
			if commitFailure && healthProjectionRevision(t, s) != projectionRevision {
				t.Fatal("failed transaction published projection revision")
			}
			nextAudits, err := s.AuditEvents(ctx)
			if err != nil || !reflect.DeepEqual(audits, nextAudits) {
				t.Fatal("renewal changed activation audits")
			}
		})
	}
}

func TestProtocolHealthBatchWriteCacheRevisionIsolation(t *testing.T) {
	s := openHealthWriteStore(t)
	ctx := context.Background()
	proofs := syntheticHealthProofs(129)
	read := func(tx *sql.Tx) []gatewaymembership.ProtocolObservation {
		t.Helper()
		items, err := s.protocolHealthOverlays(ctx, tx)
		if err != nil {
			t.Fatal(err)
		}
		return items
	}
	old, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer old.Rollback()
	if len(read(old)) != 0 {
		t.Fatal("fixture not empty")
	}
	writer, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	if err := writeProtocolHealthOverlays(ctx, writer, proofs, make([]error, len(proofs))); err != nil {
		t.Fatal(err)
	}
	if err := writer.Commit(); err != nil {
		t.Fatal(err)
	}
	current, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Rollback()
	if !reflect.DeepEqual(read(current), proofs) {
		t.Fatal("new transaction reused stale cache")
	}
	if len(read(old)) != 0 {
		t.Fatal("old repeatable-read transaction saw new proofs")
	}
	if !reflect.DeepEqual(read(current), proofs) {
		t.Fatal("old transaction poisoned current cache")
	}
}
